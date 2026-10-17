# Alert source payloads: can one Signal shape cover them?

Research for [#5](https://github.com/itsankoff/incident-copilot/issues/5), part of map [#3](https://github.com/itsankoff/incident-copilot/issues/3). Terms (Signal, Alert, Detector) are as defined in `CONTEXT.md`.

Surveyed 2026-10-17 against official docs only. Each claim links to the page it comes from.

## Answer

**Yes, one source-neutral Signal shape is feasible.** It needs a small common core and a per-source normalizer that maps each payload into that core. The normalizer also keeps the raw payload so investigation tools can still read source-specific detail. All six sources carry the same handful of ideas: a rule name, an instance identity, a two-way firing/resolved state, start and end times, free-form key/value metadata, and a link back to the source. They differ in four places, and each needs a deliberate design decision:

1. **Resource identity is not standardized anywhere.** Prometheus-style sources (Alertmanager, Grafana, Azure-managed Prometheus) and Datadog identify the workload only through label or tag *conventions* (`namespace`, `pod`, `kube_deployment`, ...). The cloud sources use a cloud resource ID: an AWS ARN plus metric dimensions, an Azure ARM ID, or a GCP monitored-resource type plus labels. No source has a first-class "Kubernetes workload" field. The Signal therefore needs a `resource` field that the normalizer fills on a best-effort basis, with the raw labels kept beside it.
2. **State is not always binary.** CloudWatch has `INSUFFICIENT_DATA` and Datadog has `Warn` and `No Data` states besides alert and recovery. Resolve notifications are opt-in or missing in several sources: GCP log-based policies never send them, CloudWatch over SNS only sends them when `OKActions` are configured, and Grafana can turn them off. The core can only promise `firing | resolved`, and a resolve may never arrive.
3. **Deduplication keys mean different things.** Some sources give a stable *series* key that is the same every time the alert re-fires: the Alertmanager/Grafana `fingerprint`, the CloudWatch alarm ARN, the Datadog monitor ID plus group. Others give a per-*episode* key that is new on each re-fire: the Azure `alertId`, the GCP `incident_id`, the Datadog `$ALERT_CYCLE_KEY`. The map's rule "re-investigation on re-fire" needs both, so the core should carry a series key and derive the episode from it plus `startsAt`.
4. **Batching differs.** Alertmanager and Grafana send a *group* of alerts in one request. The other four send one alert per request. The receiver should split batches so that one Signal equals one alert instance.

Sender authentication has **no common mechanism**. The options are shared secrets in a header or URL, OAuth2 client credentials, an HMAC signature (Grafana only), an X.509 message signature (SNS), and Entra ID tokens (Azure secure webhook). Authentication belongs to each source adapter and is not part of the Signal.

## Per-source findings

### Prometheus Alertmanager (webhook payload `version: "4"`)

Source: [Alertmanager configuration, `<webhook_config>`](https://prometheus.io/docs/alerting/latest/configuration/#webhook_config)

- **Envelope:** one POST per notification *group*. Top-level fields are `version` (`"4"`), `groupKey`, `truncatedAlerts`, `status`, `receiver`, `groupLabels`, `commonLabels`, `commonAnnotations`, `externalURL`, and `alerts[]`.
- **Per alert:** `status`, `labels`, `annotations`, `startsAt`, `endsAt`, `generatorURL`, and `fingerprint` ("fingerprint to identify the alert").
- **Resource identity:** only through labels. The rule author decides which labels exist. kube-prometheus rules conventionally carry `namespace`, `pod`, `container`, `deployment`, and similar labels.
- **Firing/resolved:** `status` is `firing` or `resolved`, both at group level and per alert. `send_resolved` defaults to `true`.
- **Dedup key:** `fingerprint` is per alert and is a hash of the alert's label set, so it stays the same across re-fires. `groupKey` identifies the group ("e.g. to deduplicate"). Each re-fire episode has a new `startsAt`.
- **Labels/annotations:** native `labels` and `annotations` maps of string to string.
- **Auth (sender side, `http_config`):** `basic_auth`, `authorization` (Bearer by default), `oauth2`, and `tls_config` (mTLS). Alertmanager does not sign the payload.

### Grafana Alerting (webhook contact point)

Sources: [Webhook notifier](https://grafana.com/docs/grafana/latest/alerting/configure-notifications/manage-contact-points/integrations/webhook-notifier/), [Group alert notifications](https://grafana.com/docs/grafana/latest/alerting/fundamentals/notifications/group-alert-notifications/), [Manage contact points](https://grafana.com/docs/grafana/latest/alerting/configure-notifications/manage-contact-points/)

- **Envelope:** a superset of the Alertmanager v4 shape. It has `receiver`, `status`, `orgId`, `alerts[]`, `groupLabels`, `commonLabels`, `commonAnnotations`, `externalURL`, `version`, `groupKey`, and `truncatedAlerts`, plus `state` (`alerting`/`ok`), `title`, and `message`.
- **Per alert:** the Alertmanager fields plus `values`, `silenceURL`, `dashboardURL`, `panelURL`, and `imageURL`.
- **Resource identity:** through labels, as in Alertmanager. An alert instance is a rule plus its label set. Instances are grouped by the policy's `Group by` labels, which default to `alertname` and `grafana_folder`.
- **Firing/resolved:** `firing` or `resolved`. A contact point can turn on "Disable resolved message", and alerts that resolve inside the group wait send no notification.
- **Dedup key:** `fingerprint` per alert and `groupKey` per group, as in Alertmanager.
- **Labels/annotations:** native maps. The **Custom Payload** template option can replace the whole body, so the adapter must assume the default payload.
- **Auth:** basic auth or an `Authorization` header (one or the other), **HMAC-SHA256 signature** (header `X-Grafana-Alerting-Signature` by default, with an optional timestamp header against replay), TLS client certificate, and extra headers. The docs warn that the URL field is not a secured field.

### AWS CloudWatch alarms (EventBridge and SNS)

Sources: [Alarm events and EventBridge](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/cloudwatch-and-eventbridge.html), [SNS notification schema](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/Notify_Users_Alarm_Changes.html#alarm-sns-schema), [Alarm tags in action payloads](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/alarm-tags-in-notifications.html), [Verifying SNS signatures](https://docs.aws.amazon.com/sns/latest/dg/sns-verify-signature-of-message.html), [API destinations](https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-api-destinations.html), [Connection auth methods](https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-target-connection-auth.html)

- **Envelope (EventBridge):** `detail-type: "CloudWatch Alarm State Change"` (or `"... Alarm Contributor State Change"` for multi-time-series and multi-contributor log alarms), `source: aws.cloudwatch`, `id`, `account`, `region`, `time`, `resources: [alarm ARN]`, and `detail`. `detail` holds `alarmName`, `state` and `previousState` (each with `value`, `reason`, `reasonData`, `timestamp`), `configuration` (metrics with `namespace`, `name`, `dimensions`; or a log query; or a composite `alarmRule`), optional `alarmContributor {id, attributes}`, `muteDetail`, and `alarmTags`. CloudWatch sends these events to EventBridge by default.
- **Envelope (SNS):** the message body has `AlarmName`, `AlarmDescription`, `AWSAccountId`, `NewStateValue`, `NewStateReason`, `StateChangeTime`, `Region`, `AlarmArn`, `OldStateValue`, and `Trigger {MetricName, Namespace, Dimensions[{name,value}], ...}`, plus `AlarmTags[{Key,Value}]` when tag propagation is on. SNS wraps this in its own envelope as a JSON string.
- **Resource identity:** the alarm ARN identifies the *alarm*, not the workload. The workload is in metric `dimensions` (for example `InstanceId`, `ClusterName`/`ServiceName` for ECS) or in `alarmContributor.attributes`. Tags can carry operator-defined workload hints.
- **Firing/resolved:** three states, `ALARM`, `OK`, and `INSUFFICIENT_DATA`, sent as transitions with the previous state included. Over SNS, a notification only goes out for states that have actions configured, so recovery needs `OKActions`.
- **Dedup key:** no per-episode ID. The series key is the alarm ARN, plus `alarmContributor.id` for contributor events. The episode can be derived from ARN plus the `state.timestamp` of the `ALARM` transition. The EventBridge `id` is unique per event and is not an alert key.
- **Labels/annotations:** dimensions (a map in EventBridge, a list in SNS), alarm tags (a map in EventBridge, a list in SNS), and `AlarmDescription` as the only free text.
- **Auth:** *SNS over HTTPS* requires a subscription confirmation and checking the message signature against an SNS-issued certificate. SignatureVersion 2 (SHA256) is recommended, and messages with an unexpected `TopicArn` should be rejected. *EventBridge API destinations* support Basic, API key, or OAuth client-credentials connections, require a public HTTPS endpoint, do not support mTLS, and time out after 5 seconds.

### Azure Monitor (common alert schema)

Sources: [Common alert schema](https://learn.microsoft.com/en-us/azure/azure-monitor/alerts/alerts-common-schema), [Action groups](https://learn.microsoft.com/en-us/azure/azure-monitor/alerts/action-groups)

- **Envelope:** `schemaId: "azureMonitorCommonAlertSchema"` and `data {essentials, alertContext, customProperties}`. Each action must opt in separately with `useCommonAlertSchema: true`.
- **Essentials:** `alertId`, `alertRule`, `alertRuleID`, `severity` (`Sev0` to `Sev4`), `signalType` (Metric/Log/Activity Log), `monitorCondition`, `monitoringService`, `alertTargetIDs`, `configurationItems`, `originAlertId`, `firedDateTime`, `resolvedDateTime`, `description`, `targetResourceGroup`, and `targetResourceType`.
- **Resource identity:** `alertTargetIDs` holds ARM resource IDs, such as an AKS cluster ID. `configurationItems` holds the resources that actually emitted the data. The workload *inside* AKS is only in `alertContext`: metric `dimensions`, log query dimensions, or, for Azure-managed Prometheus alerts, `alertContext.labels` and `alertContext.annotations`.
- **Firing/resolved:** `monitorCondition` is `Fired` or `Resolved`. `resolvedDateTime` is "currently only applicable for metric alerts".
- **Dedup key:** `alertId` is the alert *instance*, so each fire gets a new one. `originAlertId` is the instance ID from the originating monitoring service. Azure has no stable series key, so one must be derived, for example from the rule ID plus target plus dimensions.
- **Labels/annotations:** `customProperties` (key:value pairs set on the rule, `null` when unset), plus `alertContext` content that depends on the alert type: dimensions, or the Prometheus `labels`/`annotations`.
- **Auth:** the plain *Webhook* action supports only basic auth with credentials in the URI, and the endpoint must be public. *Secure webhook* uses a Microsoft Entra ID service-principal token and has no basic auth. Retries go up to 5 times on 408, 429, 503, and 504.

### Google Cloud Monitoring (webhook, schema `1.2`)

Sources: [Notification options, webhooks](https://docs.cloud.google.com/monitoring/support/notification-options), [Alerts and incidents](https://docs.cloud.google.com/monitoring/alerts/incidents-events), [AlertPolicy REST reference](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v3/projects.alertPolicies)

- **Envelope:** `version: "1.2"` and `incident`. `incident` holds `incident_id`, `scoping_project_id`, `url`, `state`, `started_at`, `ended_at`, `summary`, `severity`, `observed_value`, `threshold_value`, `resource {type, labels}`, `resource_name`, `resource_display_name`, `metric {type, displayName, labels}`, `metadata {system_labels, user_labels}`, `policy_name`, `policy_user_labels`, `condition`, `condition_name`, `documentation`, and `renotify`.
- **Resource identity:** the monitored-resource `type` plus `labels`. For example, `k8s_container` carries `cluster_name`, `namespace_name`, `pod_name`, and `container_name`, which makes it the most structured source surveyed.
- **Firing/resolved:** `state` is `open` or `closed`, and `ended_at` is set only when closed. A close notification is sent only if the policy's `notificationPrompts` includes `CLOSED`. Log-based policies are always `[OPENED]`. Incidents also close automatically after `autoClose`, which defaults to 7 days without data.
- **Dedup key:** `incident_id`, one per incident. A re-fire after close creates a new incident. Each time series that breaches gets its own incident.
- **Labels/annotations:** `resource.labels`, `metric.labels`, `metadata.system_labels`, `metadata.user_labels`, and `policy_user_labels`. `documentation` serves as the annotation or runbook text.
- **Auth:** basic auth (the endpoint must challenge with 401 and `WWW-Authenticate`) or a token in the URL query string. Public endpoints only; for private endpoints the docs point to Pub/Sub.

### Datadog monitors (Webhooks integration)

Sources: [Webhooks integration](https://docs.datadoghq.com/integrations/webhooks/), [Notification variables](https://docs.datadoghq.com/monitors/notify/variables/), [Kubernetes tag extraction](https://docs.datadoghq.com/containers/kubernetes/tag/)

- **Envelope:** the payload is **operator-templated**. A default JSON body exists but its fields are not documented, and the operator fills a custom template with `$VARIABLES`. Our adapter must therefore ship a required template and parse only that.
- **Relevant variables:** `$ALERT_ID` (monitor ID), `$ALERT_CYCLE_KEY` ("ID to link events from the time an alert triggers until it resolves"), `$AGGREG_KEY`, `$ID` (event), `$ALERT_TRANSITION`, `$ALERT_STATUS`, `$ALERT_TITLE`, `$ALERT_SCOPE` ("comma-separated list of tags triggering the alert"), `$TAGS`, `$TAGS[key]`, `$HOSTNAME`, `$ALERT_PRIORITY`, `$ALERT_METRIC`, `$ALERT_QUERY`, `$DATE`, `$LAST_UPDATED`, `$LINK`, `$EVENT_MSG`, and `$ORG_ID`.
- **Resource identity:** through tags. For multi-alert monitors, `$ALERT_SCOPE` carries the group-by tags. The Datadog Agent adds Kubernetes tags such as `kube_cluster_name`, `kube_namespace`, `kube_deployment`, `kube_stateful_set`, `pod_name`, `kube_container_name`, and `kube_ownerref_kind`/`kube_ownerref_name`.
- **Firing/resolved:** `$ALERT_TRANSITION` takes values such as `Triggered`/`Re-Triggered`, `Recovered`, `Warn`/`Re-Warn`, `No Data`/`Re-No Data`, and `Renotify`. Monitor states include Alert, Warning, No Data, Unknown, and OK.
- **Dedup key:** the series key is `$ALERT_ID` plus `$ALERT_SCOPE`. The episode key is `$ALERT_CYCLE_KEY`.
- **Labels/annotations:** tags as `key:value` strings in a comma-separated list. The message body is the only annotation-like text.
- **Auth:** basic auth embedded in the URL, custom headers (JSON), or OAuth 2.0 client credentials (Datadog fetches the token and sends it as Bearer). The docs do not mention payload signing.

## Comparison

| | Alertmanager v4 | Grafana | CloudWatch (EB / SNS) | Azure common schema | GCP Monitoring 1.2 | Datadog |
|---|---|---|---|---|---|---|
| Alerts per request | group (many) | group (many) | 1 | 1 | 1 | 1 |
| Workload identity | labels (convention) | labels (convention) | alarm ARN + metric dimensions / contributor attrs | ARM ID in `alertTargetIDs` + `alertContext` dims/labels | `resource.type` + `resource.labels` (e.g. `k8s_container`) | tags (`kube_*`, `pod_name`) via `$ALERT_SCOPE`/`$TAGS` |
| States | firing / resolved | firing / resolved | ALARM / OK / INSUFFICIENT_DATA | Fired / Resolved | open / closed | Triggered, Recovered, Warn, No Data, Renotify |
| Resolve sent? | yes by default (`send_resolved`) | yes unless disabled | EB yes; SNS only with `OKActions` | yes (`resolvedDateTime` metric alerts only) | only if `CLOSED` prompt; never for log-based | yes (`Recovered`) |
| Series key (stable across re-fires) | `fingerprint` | `fingerprint` | alarm ARN (+ contributor id) | none (derive) | none (derive from policy + resource) | `$ALERT_ID` + `$ALERT_SCOPE` |
| Episode key (new each fire) | fingerprint + `startsAt` | fingerprint + `startsAt` | ARN + ALARM `state.timestamp` | `alertId` | `incident_id` | `$ALERT_CYCLE_KEY` |
| Labels | `labels` map | `labels` map | dimensions + `alarmTags` | `customProperties`, context dims/labels | resource/metric/user/system labels | tag strings |
| Annotations / text | `annotations` map | `annotations` map | description, `reason` | `description` | `documentation`, `summary` | message / `$EVENT_MSG` |
| Severity | label (convention) | label (convention) | none | `severity` Sev0-4 | `severity` | `$ALERT_PRIORITY` P1-P5 |
| Back-link | `generatorURL`, `externalURL` | + dashboard/panel/silence URLs | none in payload (derive console URL) | none in essentials (portal links in context) | `url` | `$LINK` |
| Payload fixed? | fixed | fixed (custom template optional) | fixed | fixed (opt-in per action) | fixed | **operator template** |
| Sender auth | basic / bearer / OAuth2 / mTLS | basic / bearer / **HMAC-SHA256** / mTLS | SNS: **X.509 signature**; EB: basic / API key / OAuth2 | URI basic; Entra ID token (secure webhook) | basic (401 challenge) / query token | URL basic / headers / OAuth2 |

## Proposed common core

One Signal per alert instance. Fields marked `?` are optional because at least one source cannot fill them.

| Field | Meaning | Filled from |
|---|---|---|
| `source` | `{kind, name}`: kind is `alertmanager`, `grafana`, `cloudwatch`, `azure-monitor`, `gcp-monitoring`, `datadog`, or `detector`; name is the configured adapter instance | receiver config |
| `key` | **Series key**, stable across re-fires of the same alert on the same target | AM/Grafana `fingerprint`; CW ARN(+contributor); DD `$ALERT_ID`+scope; Azure/GCP: hash of rule ID + target + dims (derived) |
| `status` | `firing` or `resolved` | mapped. Non-binary states (`INSUFFICIENT_DATA`, `No Data`, `Warn`) are kept in `sourceState`; whether they make a Signal is adapter policy |
| `sourceState` | the source's raw state string | as received |
| `startsAt` | when this episode started; `key` + `startsAt` identify the episode | `startsAt` / `state.timestamp` / `firedDateTime` / `started_at` / `$DATE` |
| `endsAt?` | when it resolved | `endsAt` / `resolvedDateTime` / `ended_at` / OK transition time |
| `name` | rule or alert name | `alertname` label / `alarmName` / `alertRule` / `policy_name` / `$ALERT_TITLE` |
| `severity?` | raw severity string; normalize later if needed | label / `severity` / `$ALERT_PRIORITY` |
| `summary?` | human-readable text | annotations `summary`/`description` / `AlarmDescription` / `description` / `summary` / message |
| `labels` | flat `map[string]string` of every identity-bearing key/value | labels; dimensions + alarm tags; `customProperties` + context dims/labels; resource/metric/user labels; parsed `key:value` tags |
| `annotations` | flat `map[string]string` of descriptive text | annotations; `documentation`; etc. |
| `resource?` | best-effort target: `{platform, id?, cluster?, namespace?, kind?, name?}`, where `id` is an ARN, ARM ID, or GCP resource name | normalizer rules per source (e.g. `namespace`+`deployment` labels, `k8s_container` labels, `kube_*` tags) |
| `link?` | URL back to the alert in the source | `generatorURL` / `url` / `$LINK` |
| `raw` | original payload bytes, for tools and audit | as received |

What is deliberately **not** in the core: thresholds and observed values, query text, evaluation windows, group-level fields (`groupKey`, `commonLabels`), and silence or dashboard URLs. These are source-specific, live only in `raw`, and are read by source-aware investigation tools if needed. A Detector fills the same core directly: its `key` is the Detector name plus the object UID, and `resource` is always set.

## Implications for the design

- **`resource` is the field that matters most and the least reliable one.** For Kubernetes Alerts it depends on label conventions that the operator controls. The Install will need a configurable mapping from labels to resource (with defaults for kube-prometheus, `k8s_container`, and Datadog `kube_*`), and a defined behaviour when a Signal has no resolvable target: investigate without a target, or reject.
- **Treat a resolve as best-effort.** Several sources may never send one, so incident lifecycle cannot depend on receiving it.
- **Re-fire detection** uses the same `key` with a new `startsAt`. This works for all six sources, given a derived `key` for Azure and GCP.
- **Datadog needs a documented template** shipped by incident-copilot, because the payload has no fixed schema.
- **Authentication is per adapter.** The only option most sources share is a shared secret in a header (bearer or API key). GCP (query token or basic auth), plain Azure webhook (URI basic auth), and SNS (signature only) are the exceptions. The two strongest mechanisms are Grafana's HMAC and SNS's signature verification, and both are source-specific.
