# OpenAPI worklog

Tracking the practical OpenAPI spec for Adapt, optimised for Postman import and
backend testing.

## Scope notes

- Source of truth is the Go handler implementation under `internal/api/` plus
  auth middleware and supporting docs.
- Non-API HTML routes are excluded.
- Where a requested method or path does not match the implementation, the spec
  documents the implemented behaviour and this worklog notes the correction.

## Progress legend

- `done` - documented, validated, committed, and pushed
- `in progress` - currently being worked on
- `pending` - not yet documented
- `corrected` - requested method/path differed from implementation
- `not implemented` - requested route does not currently exist in handlers

## Chunk 1 - done

1. `GET /health` - done
2. `GET /health/db` - done
3. `POST /v1/auth/register` - done
4. `POST /v1/auth/session` - done
5. `GET /v1/auth/profile` - done
6. `PATCH /v1/auth/profile` - done
7. `GET /v1/jobs` - done
8. `POST /v1/jobs` - done
9. `GET /v1/jobs/{id}` - done
10. `PUT /v1/jobs/{id}` - done

## Chunk 2 - done

11. `DELETE /v1/jobs/{id}` - done
12. `GET /v1/jobs/{id}/tasks` - done
13. `GET /v1/jobs/{id}/export` - done
14. `GET /v1/schedulers` - done
15. `POST /v1/schedulers` - done
16. `GET /v1/schedulers/{id}` - done
17. `PUT /v1/schedulers/{id}` - done
18. `DELETE /v1/schedulers/{id}` - done
19. `GET /v1/shared/jobs/{token}` - done
20. `GET /v1/dashboard/stats` - done

## Chunk 3 - done

21. `GET /v1/dashboard/activity` - done
22. `GET /v1/dashboard/slow-pages` - done
23. `GET /v1/dashboard/external-redirects` - done
24. `GET /v1/metadata/metrics` - done
25. `GET /v1/organisations/invites/preview` - done
26. `GET /v1/organisations` - done
27. `POST /v1/organisations` - done
28. `POST /v1/organisations/switch` - done
29. `GET /v1/organisations/members` - done
30. `PATCH /v1/organisations/members/{id}` - done

## Chunk 4 - done

31. `DELETE /v1/organisations/members/{id}` - done
32. `POST /v1/organisations/invites/accept` - done
33. `GET /v1/organisations/invites` - done
34. `POST /v1/organisations/invites` - done
35. `DELETE /v1/organisations/invites/{id}` - done
36. `GET /v1/organisations/plan` - corrected, not implemented as `GET`; handler
    currently exposes `PUT /v1/organisations/plan`
37. `PUT /v1/organisations/plan` - done
38. `GET /v1/domains` - corrected to implemented `POST /v1/domains`, documented
39. `GET /v1/usage` - done
40. `GET /v1/usage/history` - done

## Chunk 5 - done

41. `GET /v1/plans` - done
42. `POST /v1/webhooks/webflow/{tokenOrWorkspace}` - corrected; handlers expose
    both `POST /v1/webhooks/webflow/{token}` and
    `POST /v1/webhooks/webflow/workspaces/{workspaceId}`
43. `GET /v1/integrations/slack` - done
44. `POST /v1/integrations/slack` - done
45. `GET /v1/integrations/slack/{id}` - done
46. `DELETE /v1/integrations/slack/{id}` - done
47. `GET /v1/integrations/slack/callback` - done
48. `GET /v1/integrations/webflow` - done
49. `POST /v1/integrations/webflow` - done
50. `GET /v1/integrations/webflow/{id}` - corrected to documented
    `GET /v1/integrations/webflow/{id}/sites`

## Chunk 6 - done

51. `DELETE /v1/integrations/webflow/{id}` - done
52. `GET /v1/integrations/webflow/callback` - done
53. `PUT /v1/integrations/webflow/sites/{site_id}/schedule` - done
54. `PUT /v1/integrations/webflow/sites/{site_id}/auto-publish` - done
55. `GET /v1/integrations/google` - done
56. `POST /v1/integrations/google` - done
57. `GET /v1/integrations/google/{id}` - corrected to documented
    `PATCH /v1/integrations/google/{id}`
58. `DELETE /v1/integrations/google/{id}` - done
59. `GET /v1/integrations/google/callback` - done
60. `POST /v1/integrations/google/save-property` - done

## Chunk 7 - done

61. `GET /v1/notifications` - done
62. `POST /v1/notifications/read-all` - done
63. `PATCH /v1/notifications/{id}` - corrected to documented
    `POST /v1/notifications/{id}/read`
64. `DELETE /v1/notifications/{id}` - not implemented in current handlers
65. `POST /v1/admin/reset-db` - done
66. `POST /v1/admin/reset-data` - done

## Ambiguities and follow-ups

- `GET /v1/integrations/webflow/{id}` and `GET /v1/integrations/google/{id}` are
  listed in the request but do not exist in the current handler implementations.
- Notification item `64` is also not implemented; only list, read-all, and
  per-notification `POST /read` are currently wired.
- Webflow webhook support currently spans two real path shapes; both will be
  documented for Postman usability.
- Chunk 1 validation: YAML parsed successfully via Python `yaml.safe_load`.
- Chunk 2 note: `GET /v1/jobs/{id}/export` currently returns JSON rather than a
  CSV download.
