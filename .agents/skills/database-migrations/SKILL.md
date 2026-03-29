# Database Migrations

This project uses Supabase's built-in migration system. Never duplicate schema
management in Go code.

## Creating migrations

```bash
supabase migration new descriptive_name_here
# Creates: supabase/migrations/[timestamp]_descriptive_name_here.sql
```

## Migration patterns

### Adding columns safely

```sql
ALTER TABLE jobs
ADD COLUMN IF NOT EXISTS new_field TEXT DEFAULT '';
```

### Creating indexes (non-blocking)

```sql
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_new_field
ON jobs(new_field);
```

### Removing columns

```sql
ALTER TABLE jobs DROP COLUMN IF EXISTS old_field;
```

## Deployment flow

1. Push to feature branch
2. Supabase GitHub integration auto-applies to preview database
3. Merge to main - auto-applies to production
4. No manual steps required

## Key rules

- **DO NOT** add schema management to Go code
- **DO NOT** run `supabase db push` manually
- **DO NOT** edit migration files after they're deployed
- Keep migrations additive (ADD COLUMN, CREATE INDEX)
- Use `FOR UPDATE SKIP LOCKED` for lock-free task processing
- Reference `docs/architecture/DATABASE.md` for schema details
