# Platform Authentication Architecture

## Overview

Hover will support multiple authentication entry points while maintaining a
single unified user system. Users can access via the main dashboard, Shopify
apps, or Webflow apps.

## Core Principles

1. **One User, Multiple Access Methods** - Single BB account accessible through
   different platforms
2. **Store-Level Isolation** - Complete data separation between unrelated stores
3. **organisation-Based Grouping** - Related stores grouped under one
   organisation

## Data Structure

### Structure

**Example User Access:** When User 1 logs in, they might see:

- Organisation A (ACME Corp)
- Organisation B (Bob's Bikes)
- Organisation C (Client Site)

When User 2 logs in, they might see:

- Organisation B (Bob's Bikes)

**Example Organisation Structure:**

```
Organisation A (ACME Corp)
├── Store: company-usa.myshopify.com
├── Store: company-canada.myshopify.com
└── Store: company-uk.myshopify.com

Organisation B (Bob's Bikes)
└── Store: different-client.myshopify.com

Organisation C (Client Site)
└── Site: client-site.webflow.io
```

### Key Relationships

- **Users** can have access to multiple **organisations**
- **organisations** can have multiple **Stores/Sites**
- **Stores/Sites** belong to exactly one **organisation**

## Authentication Flows

### Direct Dashboard Access

- User logs in with email/password via Supabase Auth
- Sees organisation selector if they have access to multiple orgs
- Full access to all features

### Shopify App Access

- User accesses app from within Shopify admin
- Shopify provides store context and user identity
- BB determines which organisation the store belongs to
- User sees only that organisation's data (including related stores)
- No separate BB password needed

### Webflow App Access

- Similar to Shopify flow
- Webflow provides site context
- BB maps site to organisation
- Shows relevant organisation data

## Multi-Store Scenarios

### Scenario 1: Multi-National Merchant

- Single organisation: "ACME Corp"
- Three stores: USA, Canada, UK
- Users see aggregate data across all stores
- Can switch between stores within the app
- Unified billing and settings

### Scenario 2: Agency/Partner

- Multiple separate organisations (one per client)
- User has access to many organisations
- When accessing via Store A → sees only Org A data
- When accessing via Store B → sees only Org B data
- Complete isolation between clients

### Scenario 3: Store Staff

- No individual BB account initially
- Access through store owner's organisation
- Shadow accounts auto-created on meaningful actions (create job, edit settings)
- Maintains attribution and audit trail

## Progressive Account Creation

### Browse-Only Users

- Store staff viewing data
- No BB account created
- Access via store's organisation

### Active Users

- Trigger: Creates job, edits settings, exports data
- Action: Auto-create linked BB account
- Result: Attribution tracking, potential future upgrade

## Security & Isolation

### Data Isolation Rules

1. Users can only see organisations they have explicit access to
2. Store context (from Shopify/Webflow) determines visible organisation
3. No cross-organisation data leakage
4. Each organisation has independent settings and billing

### Context Switching

- **In Platform Apps**: Platform determines context automatically
- **In BB Dashboard**: User manually selects organisation
- **API Access**: Scoped to organisation level

## Implementation Benefits

1. **Unified User Management** - Single source of truth for user data
2. **Platform Native Experience** - Users stay within their preferred platform
3. **Flexible Access Control** - Granular permissions per organisation
4. **Clean Billing** - organisation-level billing regardless of access method
5. **Attribution Tracking** - Know who did what, when
6. **Scalable Architecture** - Easily add new platforms (WordPress, Squarespace,
   etc.)

## Future Considerations

### Account Upgrades

- Shadow accounts can be claimed and upgraded to full accounts
- Store staff leaving can maintain their job history
- Seamless transition from platform-only to direct access

### Cross-Platform Analytics

- Users with both Shopify and Webflow can see unified analytics
- Single dashboard for all web properties
- Platform-specific features remain available

### Enterprise Features

- SSO integration per organisation
- Advanced role-based permissions
- API access per organisation
- White-label options per organisation
