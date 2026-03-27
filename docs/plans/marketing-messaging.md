# Hover - Product Strategy & Positioning

## Executive Summary

Hover is a post-publish quality assurance tool that automatically crawls
websites to detect broken links and warm caches after every update. Target
market: websites with 300+ pages that publish frequently, where manual checking
becomes impossible.

## Core Value Proposition

**What GNH Actually Does:**

- Crawls entire sites after publish/update via webhook triggers
- Detects broken internal links (404s, 500s) across all pages
- Warms cache to eliminate slow first-visitor experience
- Provides actionable reports with one-click fixes

**What GNH is NOT:**

- Not uptime monitoring (like UptimeRobot)
- Not infrastructure monitoring (like Better Stack)
- Not error tracking (like Sentry)
- Not just another scheduled link checker

## Competitive Landscape

### Direct Competitors

- **Ablestar Link Manager** (Shopify): $0-9/month, 2,800+ reviews, scheduled
  scans only
- **Dr. Link Check** (Shopify): $12/month, 31 reviews, basic functionality
- **SEOAnt Suite** (Shopify): $8-30/month, bundles many features

### Key Differentiators

1. **Webhook-triggered scanning** (instant post-publish vs scheduled)
2. **Full-site crawling** (all pages vs spot checks)
3. **Cache warming** (unique for Webflow)
4. **Aggressive pricing** ($5 entry point)
5. **Ease of Use & Actionable Fixes** (brand trust and simplicity are the
   long-term moat)

## Pricing Strategy

### Recommended Tiers

**7-Day Free Trial**

- Full access to chosen plan features
- No credit card required
- Automatic conversion to paid after trial

**Starter ($5/month)**

- **50 scans / month**
- Up to 250 pages per scan
- Basic broken link reports
- Email alerts

**Growth ($15/month)**

- **250 scans / month**
- Up to 1,000 pages per scan
- Broken links + performance metrics
- Cache warming (Webflow)
- Slack/email alerts

**Scale ($35/month)**

- **1,000 scans / month**
- Up to 5,000 pages per scan
- Advanced metrics (TTFB, LCP)
- API access
- Priority support
- White-label reports

### Pricing Rationale

- **No free tier** eliminates freeloaders and support burden.
- **$5 entry point** is low enough to be a no-brainer, high enough to ensure
  commitment.
- **7-day trial** proves value without enabling permanent free usage.
- **Metered usage** (scan allowances) protects infrastructure from abuse while
  being generous.
- **Value-based tiers:** Cache warming and performance metrics are kept to
  higher tiers, so we don't have to educate the lowest-price users on more
  complex features.

## Platform-Specific Positioning

### Webflow

**Lead Message:** "Make every Webflow publish instantly fast."

- Frame as an essential final step in the publishing workflow.
- Cache warming is the killer feature, broken link checking is the bonus.
- Target agencies managing multiple client sites.

### Shopify

**Lead Message:** "Find broken links before they cost you sales."

- Broken link detection is the primary painkiller.
- Performance monitoring is the bonus.
- Target stores with 300+ products and frequent updates.

## Target Customer Profile

### Ideal Customer Characteristics

- **Size:** 300-400+ pages (where manual checking fails)
- **Update Frequency:** Publishing/updating weekly or more
- **Business Impact:** Site quality is directly tied to revenue or client
  satisfaction.
- **Type:**
  - Webflow agencies/freelancers
  - High-SKU Shopify stores
  - Content-heavy sites with frequent updates
  - Flash sale/limited edition stores

### Customer Segments (Priority Order)

1. **Webflow Agencies** - Understand technical value, can bundle into
   maintenance.
2. **High-Update Shopify Stores** - Frequent product changes, broken links =
   lost sales.
3. **Marketing Teams** - Launching campaign landing pages, need confidence in
   deploys.

## Go-to-Market Strategy

### Phase 1: Months 1-3 (Target: 50 customers)

- Launch with Webflow integration.
- Manual outreach to agencies.
- **Prioritise social proof:** Get 20-30 early customers for case studies and
  testimonials. Incentivise reviews (e.g., one free month).
- Refine based on feedback.

### Phase 2: Months 4-6 (Target: 200 customers)

- Shopify app launch.
- Content marketing (case studies from Phase 1).
- Comparison content vs Ablestar.
- Early SEO efforts.

### Phase 3: Months 7-12 (Target: 500 customers)

- App store optimization.
- Affiliate/referral program.
- Scale content marketing.
- Add integration partnerships.

### Phase 4: Months 13-18 (Target: 1,000 customers)

- Compound growth from all channels.
- Potential feature expansion based on user feedback.
- Consider additional platforms.

## Messaging Framework

### Core Messages

**Primary:** "The simplest way to QA your site after every publish."

- Emphasises ease of use and automation.
- Focuses on the workflow trigger ("after every publish").

**For Shopify:** "Automatically find broken links before they cost you sales."

- Leads with the universal, high-impact pain point.

**For Webflow:** "Ship fast sites, not slow ones."

- Leads with the high-value "bonus" for the more technical audience.

### Persona-Specific Lines

- **For Agencies:** "Automate QA across all your client sites."
- **For Marketers:** "Launch campaigns with confidence."
- **For Stores:** "Protect your customer's path to purchase."

### Supporting Proof Points & Quantifiable Impact

_This data connects our features to tangible business results and should be used
in marketing materials._

- **Boost Conversions:** A 1-second page load delay can reduce conversions by
  7%. A broken link is a guaranteed lost sale for that session.
- **Improve SEO:** Eliminate 404s to maximise your crawl budget. A faster site
  improves Core Web Vitals, a known Google ranking factor.
- **Increase Speed:** Slash server response time (TTFB) by up to 90% on new
  content by eliminating the "first load penalty."

## Roadmap

### Short-Term (Launch / Must-Haves)

- Webhook-triggered crawling
- Best-in-class broken link reporting (clear, visual, actionable)
- Email/Slack alerts
- Simple, clear pricing & usage dashboard

### Mid-Term (3-9 Months / Should-Haves)

- Cache warming (Webflow specific)
- Performance metrics (simplified)
- Historical tracking & reports
- Bulk redirect management
- Portfolio / Multi-site management for agencies

### Long-Term (9+ Months / Nice-to-Haves)

- API access
- White-label reports
- Advanced performance analytics
- Integrations (Vercel, Netlify, other CMS platforms)

## Critical Success Factors

1. **Lead with Broken Links:** This is the universal painkiller. Position as QA
   automation, not another monitoring tool.
2. **Nail the Niche:** Focus relentlessly on 300+ page sites with frequent
   updates where manual QA fails.
3. **Social Proof is Everything:** Get those first 20-30 customers leaving
   reviews to build trust.
4. **Webhook Adoption:** This is the core technical moat vs. scheduled scanners.
5. **Ease of Use:** The long-term differentiator will be brand trust and a
   simple, intuitive user experience.

## Risks & Mitigation

### Risks

- Platform API changes breaking webhook integration.
- Competitors adding webhook triggers.
- Market education burden for cache warming.
- Infrastructure costs if abuse isn't prevented.

### Mitigation

- Multiple trigger options (webhook + scheduled fallback).
- Build brand and ease-of-use as differentiators beyond just features.
- Lead with broken links, educate on cache warming to qualified users.
- Per-scan page and monthly scan limits prevent abuse.

## Bottom Line

Hover can realistically achieve 1,000 paying customers in 12-18 months by:

- Starting narrow (Webflow agencies) then expanding (Shopify stores).
- Leading with understood value (broken links) while educating on advanced
  features (cache warming).
- Maintaining aggressive, but metered, pricing.
- Focusing on sites where manual QA is a clear and present pain.
