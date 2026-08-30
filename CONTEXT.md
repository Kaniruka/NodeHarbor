# Proxy Subscription Curation

This context describes how upstream proxy subscriptions are evaluated and transformed into a curated subscription for personal use on a Windows desktop.

## Language

**Upstream Subscription**:
A user-provided Clash or Mihomo YAML document containing candidate proxy nodes.
_Avoid_: Airport, provider feed, source link

**Stale Upstream Subscription**:
An upstream subscription whose latest refresh failed while a previously successful document remains temporarily eligible for evaluation.
_Avoid_: Invalid subscription, deleted subscription

**Proxy Node**:
A connectable proxy configuration taken from an upstream subscription. Distinct proxy nodes may lead to the same public exit address.
_Avoid_: IP address, line, server

**Published Proxy Group**:
A group of qualified proxy nodes generated inside a Published Subscription for automatic selection, failover, or manual selection.
_Avoid_: Subscription, upstream group

**Evaluation Run**:
A single serialized cycle that refreshes upstream subscriptions, evaluates their proxy nodes, and prepares a publication snapshot.
_Avoid_: Scan, speed test, refresh

**Test Channel**:
A temporary network path established through one proxy node for evaluation.
_Avoid_: Node, connection

**Exit Identity**:
The public IP address observed by a remote service when traffic travels through a test channel. Multiple proxy nodes may share one exit identity.
_Avoid_: Proxy node, server address

**Availability Check**:
The first evaluation stage that rejects proxy nodes unable to carry test requests reliably or within the configured latency threshold. It does not measure bulk-download throughput.
_Avoid_: IP scoring, full test, speed score

**Scoring Provider**:
An external website adapter that returns an IP score for an exit identity. Each scoring provider has its own score meaning, threshold, availability, and cache.
_Avoid_: Scoring rule, interchangeable data source

**IP Score**:
A provider-specific numeric assessment returned for an exit identity by an external IP-scoring website. Scores from different providers are not interchangeable.
_Avoid_: Node score, speed score, universal score

**Qualified Node**:
A proxy node that passes the availability check and whose exit identity meets the configured IP-score threshold.
_Avoid_: Good IP, clean node, premium node

**Published Subscription**:
A generated Clash or Mihomo YAML document containing qualified nodes and generated proxy groups, without rules or other settings inherited from upstream subscriptions.
_Avoid_: Merged configuration, airport subscription

**Publication Snapshot**:
A complete, validated version of the published subscription that replaces the previous version as one indivisible result.
_Avoid_: Partial output, working file

**Desktop Runtime**:
The local Windows environment in which NodeHarbor evaluates proxy nodes and publishes a curated subscription.
_Avoid_: Android runtime, mobile runtime, server runtime

**Managed Browser Runtime**:
A versioned browser environment used to evaluate scoring websites that require rendered browser behavior.
_Avoid_: Default browser, WebUI browser, HTTP client

**Browser Context**:
An isolated browser session state used for one proxy-node and scoring-provider combination.
_Avoid_: Browser tab, shared profile, browser process

**Browser Proxy Endpoint**:
A temporary local proxy entry that routes a Managed Browser Runtime session through one Test Channel.
_Avoid_: System proxy, global proxy, upstream subscription

**Browser Scoring Session**:
The bounded activity that uses one Browser Context to verify an Exit Identity and obtain one provider-specific IP Score.
_Avoid_: Browser visit, web scrape, generic score request
