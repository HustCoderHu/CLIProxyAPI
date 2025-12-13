# Amp CLI Integration Overview

## 1. Identity

- **What it is:** A modular routing system that integrates Amp CLI with local OAuth providers.
- **Purpose:** Enables seamless use of local API keys with Amp CLI while minimizing Amp credit usage.

## 2. High-Level Description

The Amp CLI Integration module provides intelligent routing between Amp CLI requests and local OAuth providers. It implements provider route aliases, OAuth management proxying, smart model fallback, and model mapping to optimize costs while maintaining full compatibility with Amp CLI's expected API format. The system operates with a three-tier routing logic: local providers first, model mappings second, and Amp upstream as last resort.