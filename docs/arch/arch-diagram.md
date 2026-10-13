# Architecture diagram

## Architecture

```
Alertmanager --webhook--> Agent loop <--tool calls--> MCP tool server --> Prometheus / Loki / k8s API / GitHub API
                             |  ^
                             v  |
                         LLM provider (Azure OpenAI default)
                             |
                             v
                   Ticketing adapter (ServiceNow | GitHub Issues) --approval--> Approval gate <-- Slack button
                                                                                     |
                                                                                     v
                                                                            Runbook executor --> k8s API
   All components ---> append-only audit log
```

Flow: **Trigger → Investigate → Report → Remediate → Record.**
