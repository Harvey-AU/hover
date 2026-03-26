# Architecture Documentation

System design, database schema, and API specifications for Hover.

## 📄 Documents

### Core Architecture

- **[ARCHITECTURE.md](./ARCHITECTURE.md)** - System design, components, worker
  pools, and job lifecycle
- **[DATABASE.md](./DATABASE.md)** - PostgreSQL schema, queries, and performance
  optimisation
- **[API.md](./API.md)** - RESTful API endpoints, authentication, and response
  formats

## 🏗️ System Overview

Hover uses a PostgreSQL-backed worker pool architecture for efficient URL
crawling and cache warming.

### Key Components

- **Worker Pool** - Concurrent job processing with configurable limits
- **Job Queue** - PostgreSQL-based task queue with row-level locking
- **API Layer** - RESTful endpoints with JWT authentication
- **Crawler** - Intelligent sitemap processing and link discovery

## 🔗 Related Documentation

- [Development Setup](../development/DEVELOPMENT.md) - Get the system running
- [Testing Strategy](../testing/) - How to test the architecture
- [Database Operations](./DATABASE.md) - Schema and query details
