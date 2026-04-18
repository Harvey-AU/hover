package jobs

import (
	"context"
	"database/sql"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

)

// DomainLimiterConfig controls adaptive throttling behaviour.
type DomainLimiterConfig struct {
	BaseDelay             time.Duration
	DelayStep             time.Duration
	SuccessProbeThreshold int
	MaxAdaptiveDelay      time.Duration
	ConcurrencyStep       time.Duration
	PersistInterval       time.Duration
	MaxBlockingRetries    int
	CancelRateLimitJobs   bool
	CancelStreakThreshold int
	CancelDelayThreshold  time.Duration
	RobotsDelayMultiplier float64
}

func defaultDomainLimiterConfig() DomainLimiterConfig {
	cfg := DomainLimiterConfig{
		BaseDelay:             50 * time.Millisecond,
		DelayStep:             500 * time.Millisecond,
		SuccessProbeThreshold: 5,
		MaxAdaptiveDelay:      60 * time.Second,
		ConcurrencyStep:       5 * time.Second,
		PersistInterval:       30 * time.Second,
		MaxBlockingRetries:    3,
		CancelRateLimitJobs:   false,
		CancelStreakThreshold: 20,
		CancelDelayThreshold:  60 * time.Second,
		RobotsDelayMultiplier: 0.5,
	}

	if v, ok := os.LookupEnv("GNH_RATE_LIMIT_BASE_DELAY_MS"); ok {
		if ms, err := strconv.Atoi(v); err == nil && ms >= 0 {
			cfg.BaseDelay = time.Duration(ms) * time.Millisecond
		}
	}
	if v, ok := os.LookupEnv("GNH_RATE_LIMIT_MAX_DELAY_SECONDS"); ok {
		if sec, err := strconv.Atoi(v); err == nil && sec > 0 {
			cfg.MaxAdaptiveDelay = time.Duration(sec) * time.Second
		}
	}
	if v, ok := os.LookupEnv("GNH_RATE_LIMIT_SUCCESS_THRESHOLD"); ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.SuccessProbeThreshold = n
		}
	}
	if v, ok := os.LookupEnv("GNH_RATE_LIMIT_DELAY_STEP_MS"); ok {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			cfg.DelayStep = time.Duration(ms) * time.Millisecond
		}
	}
	if v, ok := os.LookupEnv("GNH_RATE_LIMIT_MAX_RETRIES"); ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.MaxBlockingRetries = n
		}
	}
	if v, ok := os.LookupEnv("GNH_RATE_LIMIT_CANCEL_THRESHOLD"); ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.CancelStreakThreshold = n
		}
	}
	if v, ok := os.LookupEnv("GNH_RATE_LIMIT_CANCEL_DELAY_SECONDS"); ok {
		if sec, err := strconv.Atoi(v); err == nil && sec >= 0 {
			cfg.CancelDelayThreshold = time.Duration(sec) * time.Second
		}
	}
	if v, ok := os.LookupEnv("GNH_RATE_LIMIT_CANCEL_ENABLED"); ok {
		cfg.CancelRateLimitJobs = v == "1" || v == "true" || v == "TRUE"
	}
	if v, ok := os.LookupEnv("GNH_ROBOTS_DELAY_MULTIPLIER"); ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 && f <= 1.0 {
			cfg.RobotsDelayMultiplier = f
		}
	}

	return cfg
}

// DomainLimiter coordinates request pacing across workers for each domain.
type DomainLimiter struct {
	cfg     DomainLimiterConfig
	dbQueue DbQueueInterface

	mu      sync.Mutex
	domains map[string]*domainState

	now func() time.Time
}

// DomainRequest describes a request against a domain that needs throttling.
type DomainRequest struct {
	Domain         string
	JobID          string
	RobotsDelay    time.Duration
	JobConcurrency int
}

// DomainPermit is returned by Acquire and must be released after the request completes.
type DomainPermit struct {
	limiter *DomainLimiter
	domain  string
	jobID   string
	delay   time.Duration
}

func newDomainLimiter(dbQueue DbQueueInterface) *DomainLimiter {
	cfg := defaultDomainLimiterConfig()
	jobsLog.Info("Domain limiter initialised", "robots_delay_multiplier", cfg.RobotsDelayMultiplier)
	return &DomainLimiter{
		cfg:     cfg,
		dbQueue: dbQueue,
		domains: make(map[string]*domainState),
		now:     time.Now,
	}
}

// Seed initialises limiter state for a domain with persisted values.
func (dl *DomainLimiter) Seed(domain string, baseDelaySeconds int, adaptiveDelaySeconds int, floorSeconds int) {
	state := dl.getOrCreateState(domain)
	state.mu.Lock()
	defer state.mu.Unlock()

	base := max(time.Duration(baseDelaySeconds)*time.Second, dl.cfg.BaseDelay)
	state.baseDelay = base

	adaptive := min(max(time.Duration(adaptiveDelaySeconds)*time.Second, base), dl.cfg.MaxAdaptiveDelay)
	state.adaptiveDelay = adaptive

	floor := min(max(time.Duration(floorSeconds)*time.Second, 0), adaptive)
	state.delayFloor = floor
}

// Acquire waits until the caller is allowed to perform a request against the domain.
func (dl *DomainLimiter) Acquire(ctx context.Context, req DomainRequest) (*DomainPermit, error) {
	if req.Domain == "" {
		return &DomainPermit{limiter: dl, domain: "", jobID: req.JobID}, nil
	}

	state := dl.getOrCreateState(req.Domain)
	delay, err := state.acquire(ctx, dl.cfg, dl.now, req)
	if err != nil {
		return nil, err
	}

	return &DomainPermit{
		limiter: dl,
		domain:  req.Domain,
		jobID:   req.JobID,
		delay:   delay,
	}, nil
}

// TryAcquire attempts to acquire a domain permit without blocking on the rate-limit
// time window. Returns (permit, true) if the domain is available now, or (nil, false)
// if the domain is within a delay window — the caller should requeue the task as waiting.
// If the time window is open but per-job concurrency is exhausted, it waits for a slot
// to free up (bounded by the duration of in-flight HTTP requests for that domain).
func (dl *DomainLimiter) TryAcquire(req DomainRequest) (*DomainPermit, bool) {
	if req.Domain == "" {
		return &DomainPermit{limiter: dl, domain: "", jobID: req.JobID}, true
	}

	state := dl.getOrCreateState(req.Domain)
	delay, ok := state.tryAcquire(dl.cfg, dl.now, req)
	if !ok {
		return nil, false
	}

	return &DomainPermit{
		limiter: dl,
		domain:  req.Domain,
		jobID:   req.JobID,
		delay:   delay,
	}, true
}

// Release notifies the limiter about the outcome of a request.
func (p *DomainPermit) Release(success bool, rateLimited bool) {
	if p == nil || p.limiter == nil || p.domain == "" {
		return
	}
	p.limiter.release(p.domain, p.jobID, success, rateLimited)
}

// UpdateRobotsDelay allows adjusting the base delay when robots.txt changes.
func (dl *DomainLimiter) UpdateRobotsDelay(domain string, delaySeconds int) {
	state := dl.getOrCreateState(domain)
	state.mu.Lock()
	defer state.mu.Unlock()

	base := max(time.Duration(delaySeconds)*time.Second, dl.cfg.BaseDelay)
	state.baseDelay = base
	if state.adaptiveDelay < base {
		state.adaptiveDelay = base
	}
}

// EstimatedWait returns the estimated time until the domain is available for requests.
// Returns 0 if the domain is available immediately or unknown.
func (dl *DomainLimiter) EstimatedWait(domain string) time.Duration {
	if domain == "" {
		return 0
	}

	dl.mu.Lock()
	state, exists := dl.domains[domain]
	dl.mu.Unlock()

	if !exists {
		return 0
	}

	state.mu.Lock()
	defer state.mu.Unlock()

	now := dl.now()
	waitUntil := state.nextAvailable
	if state.backoffUntil.After(waitUntil) {
		waitUntil = state.backoffUntil
	}

	if waitUntil.After(now) {
		return waitUntil.Sub(now)
	}
	return 0
}

// Domain state ---------------------------------------------------------------------------------

type domainState struct {
	mu   sync.Mutex
	cond *sync.Cond

	baseDelay     time.Duration
	adaptiveDelay time.Duration
	delayFloor    time.Duration

	errorStreak   int
	successStreak int

	nextAvailable time.Time
	backoffUntil  time.Time

	lastPersist time.Time

	probing       bool
	probePrevious time.Duration
	probeTarget   time.Duration

	jobStates map[string]*jobDomainState
}

type jobDomainState struct {
	original int
	allowed  int
	active   int
}

func newDomainState(base time.Duration) *domainState {
	ds := &domainState{
		baseDelay: base,
		jobStates: make(map[string]*jobDomainState),
	}
	ds.cond = sync.NewCond(&ds.mu)
	return ds
}

func (dl *DomainLimiter) getOrCreateState(domain string) *domainState {
	dl.mu.Lock()
	defer dl.mu.Unlock()

	if state, ok := dl.domains[domain]; ok {
		return state
	}

	state := newDomainState(dl.cfg.BaseDelay)
	dl.domains[domain] = state
	return state
}

func (ds *domainState) ensureJobState(jobID string, concurrency int) *jobDomainState {
	js, ok := ds.jobStates[jobID]
	if !ok {
		js = &jobDomainState{original: concurrency, allowed: concurrency}
		ds.jobStates[jobID] = js
	}
	js.original = concurrency
	if js.allowed <= 0 {
		js.allowed = concurrency
	}
	return js
}

func (ds *domainState) effectiveDelay(cfg DomainLimiterConfig) time.Duration {
	delay := min(max(ds.adaptiveDelay, ds.baseDelay), cfg.MaxAdaptiveDelay)
	return delay
}

func (ds *domainState) computeAllowedConcurrency(cfg DomainLimiterConfig, jobConcurrency int) int {
	base := max(ds.baseDelay, cfg.BaseDelay)
	effective := ds.effectiveDelay(cfg)
	if effective <= base {
		return jobConcurrency
	}

	diff := effective - base
	reduction := int(diff / cfg.ConcurrencyStep)
	allowed := max(jobConcurrency-reduction, 1)
	return allowed
}

// applyRobotsDelay updates ds.baseDelay (and keeps ds.adaptiveDelay >= ds.baseDelay)
// from the robots.txt crawl delay, applying the configured multiplier.
// Must be called with ds.mu held.
func (ds *domainState) applyRobotsDelay(cfg DomainLimiterConfig, robotsDelay time.Duration) {
	if robotsDelay > 0 {
		robots := robotsDelay
		multiplierActive := cfg.RobotsDelayMultiplier > 0 && cfg.RobotsDelayMultiplier < 1.0
		if multiplierActive {
			robots = time.Duration(float64(robots) * cfg.RobotsDelayMultiplier)
		}
		if robots < cfg.BaseDelay {
			robots = cfg.BaseDelay
		}
		if multiplierActive {
			ds.baseDelay = robots
		} else if robots > ds.baseDelay {
			ds.baseDelay = robots
		}
	}
	if ds.adaptiveDelay < ds.baseDelay {
		ds.adaptiveDelay = ds.baseDelay
	}
}

// tryAcquire is the non-blocking variant of acquire. It returns (0, false) immediately
// if the domain's rate-limit time window has not yet elapsed, instead of sleeping.
// Once the time window is clear, it waits for a concurrency slot if needed (bounded
// by the duration of in-flight HTTP requests for that domain).
func (ds *domainState) tryAcquire(cfg DomainLimiterConfig, nowFn func() time.Time, req DomainRequest) (time.Duration, bool) {
	ds.mu.Lock()
	if req.JobConcurrency <= 0 {
		req.JobConcurrency = 1
	}

	now := nowFn()
	ds.applyRobotsDelay(cfg, req.RobotsDelay)

	waitUntil := ds.nextAvailable
	if ds.backoffUntil.After(waitUntil) {
		waitUntil = ds.backoffUntil
	}
	if waitUntil.After(now) {
		// Domain is in a rate-limit window — return immediately so the worker
		// can requeue the task and pick work from a different domain.
		ds.mu.Unlock()
		return 0, false
	}

	// Time window is clear. Wait for a concurrency slot if needed — cond.Wait()
	// blocks until Release() broadcasts, so this can last as long as an in-flight
	// HTTP request (seconds) if all slots are held.
	for {
		js := ds.ensureJobState(req.JobID, req.JobConcurrency)
		js.allowed = ds.computeAllowedConcurrency(cfg, req.JobConcurrency)
		if js.active < js.allowed {
			js.active++
			delay := ds.effectiveDelay(cfg)
			ds.nextAvailable = now.Add(delay)
			ds.mu.Unlock()
			return delay, true
		}
		ds.cond.Wait()
		// After waking, re-check the time window — another worker may have advanced it.
		now = nowFn()
		waitUntil = ds.nextAvailable
		if ds.backoffUntil.After(waitUntil) {
			waitUntil = ds.backoffUntil
		}
		if waitUntil.After(now) {
			ds.mu.Unlock()
			return 0, false
		}
	}
}

func (ds *domainState) acquire(ctx context.Context, cfg DomainLimiterConfig, nowFn func() time.Time, req DomainRequest) (time.Duration, error) {
	ds.mu.Lock()
	if req.JobConcurrency <= 0 {
		req.JobConcurrency = 1
	}

	for {
		now := nowFn()
		ds.applyRobotsDelay(cfg, req.RobotsDelay)

		waitUntil := ds.nextAvailable
		if ds.backoffUntil.After(waitUntil) {
			waitUntil = ds.backoffUntil
		}
		if waitUntil.After(now) {
			wait := waitUntil.Sub(now)
			ds.mu.Unlock()
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return 0, ctx.Err()
			}
			ds.mu.Lock()
			continue
		}

		js := ds.ensureJobState(req.JobID, req.JobConcurrency)
		js.allowed = ds.computeAllowedConcurrency(cfg, req.JobConcurrency)
		if js.active >= js.allowed {
			ds.cond.Wait()
			continue
		}

		js.active++
		delay := ds.effectiveDelay(cfg)
		ds.nextAvailable = now.Add(delay)
		ds.mu.Unlock()
		return delay, nil
	}
}

func (dl *DomainLimiter) release(domain string, jobID string, success bool, rateLimited bool) {
	state := dl.getOrCreateState(domain)

	state.mu.Lock()
	now := dl.now()

	js, ok := state.jobStates[jobID]
	if ok {
		if js.active > 0 {
			js.active--
		}
		state.cond.Broadcast()
	}

	var needPersist bool

	oldAdaptive := state.adaptiveDelay
	oldFloor := state.delayFloor

	if rateLimited {
		state.successStreak = 0
		state.errorStreak++
		if state.probing {
			state.adaptiveDelay = state.probePrevious
			if state.delayFloor < state.probeTarget {
				state.delayFloor = state.probeTarget
			}
			state.probing = false
		}
		nextDelay := min(state.adaptiveDelay+dl.cfg.DelayStep, dl.cfg.MaxAdaptiveDelay)
		if nextDelay > state.adaptiveDelay {
			state.adaptiveDelay = nextDelay
			needPersist = true
		}
		state.backoffUntil = now.Add(state.adaptiveDelay)
	} else if success {
		state.errorStreak = 0
		state.successStreak++
		if state.probing {
			// Probe succeeded, accept lower delay
			state.probing = false
			needPersist = true
		} else if state.successStreak >= dl.cfg.SuccessProbeThreshold {
			proposed := max(max(state.adaptiveDelay-dl.cfg.DelayStep, state.delayFloor), state.baseDelay)
			if proposed < state.adaptiveDelay {
				state.probing = true
				state.probePrevious = state.adaptiveDelay
				state.probeTarget = proposed
				state.adaptiveDelay = proposed
				state.successStreak = 0
				needPersist = true
			}
		}
	} else {
		// Non rate-limit failure
		state.successStreak = 0
		state.errorStreak = 0
	}

	adaptiveSeconds := int(state.adaptiveDelay / time.Second)
	floorSeconds := int(state.delayFloor / time.Second)

	adaptiveChanged := state.adaptiveDelay != oldAdaptive
	floorChanged := state.delayFloor != oldFloor
	errorStreak := state.errorStreak
	successStreak := state.successStreak

	shouldPersist := needPersist && (state.lastPersist.IsZero() || now.Sub(state.lastPersist) >= dl.cfg.PersistInterval)
	if shouldPersist {
		state.lastPersist = now
	}
	state.mu.Unlock()

	if adaptiveChanged {
		jobsLog.Info("Updated domain adaptive delay",
			"domain", domain,
			"adaptive_delay_seconds", adaptiveSeconds,
			"previous_delay_seconds", int(oldAdaptive/time.Second),
			"error_streak", errorStreak,
			"success_streak", successStreak,
		)
	}
	if floorChanged {
		jobsLog.Debug("Updated domain delay floor",
			"domain", domain,
			"delay_floor_seconds", floorSeconds,
			"previous_floor_seconds", int(oldFloor/time.Second),
		)
	}

	if shouldPersist {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := dl.persistDomain(ctx, domain, adaptiveSeconds, floorSeconds); err != nil {
			jobsLog.Warn("Failed to persist adaptive delay", "error", err, "domain", domain)
		}
	}
}

func (dl *DomainLimiter) persistDomain(ctx context.Context, domain string, adaptiveDelay int, floor int) error {
	if dl.dbQueue == nil {
		return nil
	}

	return dl.dbQueue.Execute(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
            UPDATE domains
            SET adaptive_delay_seconds = $1,
                adaptive_delay_floor_seconds = $2
            WHERE name = $3
        `, adaptiveDelay, floor, domain)
		return err
	})
}

// GetEffectiveConcurrency returns the current effective concurrency for a job on a domain
// Returns 0 if no override exists (use configured concurrency)
func (dl *DomainLimiter) GetEffectiveConcurrency(jobID string, domain string) int {
	if domain == "" {
		return 0
	}

	dl.mu.Lock()
	state, exists := dl.domains[domain]
	dl.mu.Unlock()

	if !exists {
		return 0
	}

	state.mu.Lock()
	defer state.mu.Unlock()

	js, ok := state.jobStates[jobID]
	if !ok {
		return 0
	}

	return js.allowed
}

// Helper utility --------------------------------------------------------------------------------

// IsRateLimitError returns true when error indicates an HTTP 429/403/503 blocking response.
func IsRateLimitError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "429") ||
		strings.Contains(strings.ToLower(err.Error()), "too many requests") ||
		strings.Contains(strings.ToLower(err.Error()), "rate limit") ||
		strings.Contains(strings.ToLower(err.Error()), "403") ||
		strings.Contains(strings.ToLower(err.Error()), "503")
}
