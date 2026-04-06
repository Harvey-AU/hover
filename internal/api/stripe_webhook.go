package api

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	stripe "github.com/stripe/stripe-go/v82"
	stripesubscription "github.com/stripe/stripe-go/v82/subscription"
	"github.com/stripe/stripe-go/v82/webhook"
)

// StripeWebhook handles incoming Stripe webhook events.
// POST /webhooks/stripe — no auth, signature verified internally.
func (h *Handler) StripeWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		MethodNotAllowed(w, r)
		return
	}

	if h.StripeWebhookSecret == "" {
		log.Error().Msg("Stripe webhook secret not configured — rejecting event")
		http.Error(w, "webhook not configured", http.StatusServiceUnavailable)
		return
	}

	const maxBodyBytes = 65536
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to read Stripe webhook body")
		BadRequest(w, r, "Failed to read request body")
		return
	}

	event, err := webhook.ConstructEvent(body, r.Header.Get("Stripe-Signature"), h.StripeWebhookSecret)
	if err != nil {
		log.Warn().Err(err).Msg("Stripe webhook signature verification failed")
		http.Error(w, "invalid signature", http.StatusBadRequest)
		return
	}

	logger := log.With().Str("stripe_event_id", event.ID).Str("stripe_event_type", string(event.Type)).Logger()
	logger.Info().Msg("Received Stripe webhook event")

	switch event.Type {
	case "checkout.session.completed":
		h.handleCheckoutSessionCompleted(r, event, logger)
	case "customer.subscription.updated":
		h.handleSubscriptionUpdated(r, event, logger)
	case "customer.subscription.deleted":
		h.handleSubscriptionDeleted(r, event, logger)
	case "invoice.payment_failed":
		h.handleInvoicePaymentFailed(event, logger)
	default:
		logger.Debug().Msg("Unhandled Stripe event type — ignoring")
	}

	// Always return 200 to acknowledge receipt.
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) handleCheckoutSessionCompleted(r *http.Request, event stripe.Event, logger zerolog.Logger) {
	var sess stripe.CheckoutSession
	if err := json.Unmarshal(event.Data.Raw, &sess); err != nil {
		logger.Error().Err(err).Msg("Failed to unmarshal checkout.session.completed")
		return
	}

	orgID := sess.ClientReferenceID
	if orgID == "" && sess.Customer != nil {
		// Fallback: look up org by customer ID.
		id, err := h.DB.GetOrganisationIDByStripeCustomerID(r.Context(), sess.Customer.ID)
		if err != nil {
			logger.Error().Err(err).Str("customer_id", sess.Customer.ID).Msg("Cannot resolve organisation from Stripe customer")
			return
		}
		orgID = id
	}
	if orgID == "" {
		logger.Error().Msg("checkout.session.completed: no organisation ID found — skipping")
		return
	}

	// Persist customer ID if not already stored.
	if sess.Customer != nil {
		if err := h.DB.SetStripeCustomerID(r.Context(), orgID, sess.Customer.ID); err != nil {
			logger.Error().Err(err).Str("org_id", orgID).Msg("Failed to store Stripe customer ID")
		}
	}

	// Persist subscription ID and activate plan.
	if sess.Subscription != nil {
		subID := sess.Subscription.ID
		if err := h.DB.SetStripeSubscriptionID(r.Context(), orgID, subID); err != nil {
			logger.Error().Err(err).Str("org_id", orgID).Msg("Failed to store Stripe subscription ID")
		}

		// The subscription in checkout.session.completed is not expanded —
		// fetch it directly to get the line items and price ID.
		sub, err := stripesubscription.Get(subID, nil)
		if err != nil {
			logger.Error().Err(err).Str("subscription_id", subID).Msg("Failed to fetch subscription from Stripe")
			return
		}

		if len(sub.Items.Data) == 0 {
			logger.Error().Str("subscription_id", subID).Msg("Subscription has no line items — cannot activate plan")
			return
		}

		if sub.Items.Data[0].Price == nil {
			logger.Error().Str("subscription_id", subID).Msg("Subscription line item has no price — cannot activate plan")
			return
		}

		priceID := sub.Items.Data[0].Price.ID
		plan, err := h.DB.GetPlanByStripePriceID(r.Context(), priceID)
		if err != nil {
			logger.Error().Err(err).Str("price_id", priceID).Msg("Cannot resolve plan from Stripe price")
			return
		}
		if err := h.DB.SetOrganisationPlan(r.Context(), orgID, plan.ID); err != nil {
			logger.Error().Err(err).Str("org_id", orgID).Str("plan_id", plan.ID).Msg("Failed to update organisation plan")
			return
		}
		logger.Info().Str("org_id", orgID).Str("plan", plan.Name).Msg("Organisation plan activated via checkout")
	}
}

func (h *Handler) handleSubscriptionUpdated(r *http.Request, event stripe.Event, logger zerolog.Logger) {
	var sub stripe.Subscription
	if err := json.Unmarshal(event.Data.Raw, &sub); err != nil {
		logger.Error().Err(err).Msg("Failed to unmarshal customer.subscription.updated")
		return
	}

	if sub.Customer == nil {
		logger.Error().Msg("subscription.updated: missing customer — skipping")
		return
	}

	orgID, err := h.DB.GetOrganisationIDByStripeCustomerID(r.Context(), sub.Customer.ID)
	if err != nil {
		logger.Error().Err(err).Str("customer_id", sub.Customer.ID).Msg("Cannot resolve organisation")
		return
	}

	if len(sub.Items.Data) == 0 {
		logger.Warn().Str("org_id", orgID).Msg("subscription.updated: no line items — skipping plan update")
		return
	}

	if sub.Items.Data[0].Price == nil {
		logger.Warn().Str("org_id", orgID).Msg("subscription.updated: no price on line item — skipping plan update")
		return
	}

	priceID := sub.Items.Data[0].Price.ID
	plan, err := h.DB.GetPlanByStripePriceID(r.Context(), priceID)
	if err != nil {
		logger.Error().Err(err).Str("price_id", priceID).Msg("Cannot resolve plan from Stripe price")
		return
	}

	if err := h.DB.SetOrganisationPlan(r.Context(), orgID, plan.ID); err != nil {
		logger.Error().Err(err).Str("org_id", orgID).Str("plan_id", plan.ID).Msg("Failed to update organisation plan")
		return
	}
	logger.Info().Str("org_id", orgID).Str("plan", plan.Name).Msg("Organisation plan updated via subscription change")
}

func (h *Handler) handleSubscriptionDeleted(r *http.Request, event stripe.Event, logger zerolog.Logger) {
	var sub stripe.Subscription
	if err := json.Unmarshal(event.Data.Raw, &sub); err != nil {
		logger.Error().Err(err).Msg("Failed to unmarshal customer.subscription.deleted")
		return
	}

	if sub.Customer == nil {
		logger.Error().Msg("subscription.deleted: missing customer — skipping")
		return
	}

	orgID, err := h.DB.GetOrganisationIDByStripeCustomerID(r.Context(), sub.Customer.ID)
	if err != nil {
		logger.Error().Err(err).Str("customer_id", sub.Customer.ID).Msg("Cannot resolve organisation")
		return
	}

	freePlanID, err := h.DB.GetFreePlanID(r.Context())
	if err != nil {
		logger.Error().Err(err).Msg("Failed to fetch free plan ID for subscription cancellation")
		return
	}

	if err := h.DB.SetOrganisationPlan(r.Context(), orgID, freePlanID); err != nil {
		logger.Error().Err(err).Str("org_id", orgID).Msg("Failed to revert organisation to free plan")
		return
	}
	logger.Info().Str("org_id", orgID).Msg("Organisation reverted to free plan — subscription cancelled")
}

func (h *Handler) handleInvoicePaymentFailed(event stripe.Event, logger zerolog.Logger) {
	var inv stripe.Invoice
	if err := json.Unmarshal(event.Data.Raw, &inv); err != nil {
		logger.Error().Err(err).Msg("Failed to unmarshal invoice.payment_failed")
		return
	}
	customerID := ""
	if inv.Customer != nil {
		customerID = inv.Customer.ID
	}
	logger.Warn().
		Str("invoice_id", inv.ID).
		Str("customer_id", customerID).
		Msg("Stripe invoice payment failed — Stripe will retry per dunning schedule")
}
