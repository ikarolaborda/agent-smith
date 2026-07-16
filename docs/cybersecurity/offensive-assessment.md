# Authorized Offensive Security Assessment

This manual supports defensive, contractually authorized security assessment.
It is not authorization to test a system. Written permission, scope, rules of
engagement, and applicable law control every action. When scope or impact is
unclear, stop and contact the named authority.

The goal is evidence that helps an owner reduce risk: identify reachable
weaknesses, validate realistic impact with the least harmful action, explain
root cause, and verify remediation.

## 1. Non-Negotiable Authorization Boundary

Before active testing, obtain a written authorization package that identifies:

- legal owner and executive or delegated sponsor;
- assessment organization and named testers;
- exact domains, applications, APIs, IP ranges, cloud accounts,
  subscriptions, projects, tenants, wireless networks, and physical locations;
- in-scope source repositories, build pipelines, identities, and third parties;
- explicit exclusions and shared infrastructure;
- start/end times, time zone, maintenance windows, and blackout periods;
- permitted test types and separately approved high-impact techniques;
- tester source addresses and accounts;
- data access, creation, modification, retention, and deletion rules;
- notification, escalation, emergency, and stop-work contacts;
- reporting classification and evidence recipients;
- third-party and cloud-provider policy approval.

An asset's public reachability does not make it in scope. A hostname resolving
into an approved IP range does not automatically authorize every colocated
virtual host. Acquiring a credential or pivot path does not expand scope.

### Activities requiring explicit separate approval

Treat these as prohibited unless the rules of engagement name them:

- denial-of-service, stress, resource-exhaustion, or destructive testing;
- phishing, pretexting, credential harvesting, or other social engineering;
- physical entry or hardware interaction;
- persistence, covert command channels, or bypass of monitoring;
- modification or deletion of production records;
- bulk access to personal, regulated, or customer data;
- password spraying or brute force;
- testing employees' personal accounts or devices;
- crossing into a supplier, customer, or cloud service outside the contract.

### Stop conditions

Stop the affected activity immediately when:

- availability, integrity, or safety degrades;
- a technique reaches an out-of-scope asset or identity;
- live secrets, regulated data, or unrelated tenant data appear unexpectedly;
- an action may cause irreversible or widespread change;
- monitoring or incident response cannot distinguish the test safely;
- authorization expires or an authorized contact invokes stop-work;
- the observed system differs materially from the approved architecture.

Preserve minimal evidence, record the time and action, notify the designated
contact, and wait for explicit direction.

## 2. Rules of Engagement as an Executable Control

Translate the authorization into a test matrix:

| Dimension | Required decision |
|---|---|
| Asset | Exact identifier and owner |
| Environment | Production, staging, development, or lab |
| Technique | Allowed, approval required, or prohibited |
| Identity | Anonymous, normal user, privileged test role, or service account |
| Source | Approved tester host or address |
| Rate | Request, connection, and concurrency ceiling |
| Data | Synthetic only, canary records, or approved production access |
| Time | Window and time zone |
| Escalation | Contact and maximum response time |
| Evidence | Capture, encryption, retention, and deletion requirements |

Enforce the matrix in tooling where possible: target allowlists, rate limits,
safe concurrency defaults, expiring credentials, and automatic stop times.
Keep a human-readable copy available during the assessment.

### Production safety controls

- Prefer passive review, source analysis, configuration inspection, and
  synthetic transactions before active probes.
- Use dedicated test accounts and canary records.
- Coordinate backups and recovery readiness without assuming a backup makes a
  destructive test acceptable.
- Start at low request rates and observe system health.
- Identify fragile legacy systems and safety-critical dependencies.
- Use a shared test identifier so defenders can correlate activity.
- Maintain a live activity log with timestamps in a common time reference.
- Pause when telemetry contradicts the assumed impact.

## 3. Assessment Lifecycle

### Phase 1: prepare

- confirm authorization and cloud or supplier policies;
- identify business-critical functions and sensitive data;
- collect architecture, data-flow, identity, network, and deployment diagrams;
- agree severity model, report format, and retest expectations;
- establish encrypted evidence storage and access control;
- test emergency communications;
- verify scanner and tool settings against target allowlists.

### Phase 2: model threats and attack surface

- enumerate assets, entry points, trust boundaries, identities, data flows, and
  administrative planes;
- identify plausible threat actors and capabilities;
- develop abuse cases and expected controls;
- map hypotheses to test cases and evidence requirements;
- prioritize paths with business impact, not merely exposed ports.

### Phase 3: discover and review

- reconcile supplied inventory with DNS, certificates, cloud inventory,
  routing, repositories, and deployment configuration;
- review source, infrastructure-as-code, identity policy, and logs where
  authorized;
- identify undocumented assets and ownership gaps;
- validate versions and configuration rather than treating a scanner banner as
  proof.

### Phase 4: test controls

Test authentication, authorization, isolation, input handling, secrets,
network boundaries, logging, recovery, and business invariants. Begin with the
least invasive technique that can answer the question.

### Phase 5: validate exploitability

Confirm that a weakness is reachable and has meaningful security impact using
synthetic or canary data. Stop at the minimum proof. Do not turn a finding into
an unrestricted foothold merely because it is technically possible.

### Phase 6: report and remediate

Provide reproducible evidence, affected scope, prerequisites, business impact,
root cause, concrete remediation, compensating controls, and a regression test.
Separate confirmed findings from observations and unverified hypotheses.

### Phase 7: retest and close

Retest the original path and nearby variants, verify the root cause is removed,
check that the fix did not create a new bypass, close evidence according to
retention policy, and document residual risk.

## 4. Threat Modeling for Assessment

Threat modeling turns an asset list into prioritized security questions.

### Build the model

For each system, identify:

- valuable assets and unacceptable outcomes;
- actors, roles, service identities, and administrators;
- processes, data stores, queues, APIs, and external dependencies;
- trust boundaries and privilege transitions;
- management plane versus data plane;
- data classification and lifecycle;
- security assumptions and compensating controls;
- observability available to detect misuse.

Use a data-flow diagram where it clarifies boundaries. Every flow should state
protocol, identity, authorization decision, data sensitivity, and encryption
expectation.

### STRIDE as a prompt

STRIDE helps generate questions:

- **Spoofing**: can an actor impersonate another identity or service?
- **Tampering**: can data, code, configuration, or messages be altered?
- **Repudiation**: can consequential action occur without attributable,
  integrity-protected evidence?
- **Information disclosure**: can data cross its intended audience or tenant?
- **Denial of service**: can bounded input exhaust a shared resource?
- **Elevation of privilege**: can an actor gain a capability outside its role?

STRIDE is not a risk score or a complete checklist. Add domain-specific abuse
cases: fraud, workflow bypass, unsafe automation, model or data poisoning,
privacy harm, and supply-chain compromise.

### Attack paths

Model a path as prerequisites and control transitions:

1. initial reachable surface;
2. identity or execution context;
3. control expected at the boundary;
4. weakness that may defeat the control;
5. next capability;
6. potential business impact;
7. detection and recovery opportunities.

Map observed behavior to MITRE ATT&CK when it improves communication with
defenders. Do not force every finding into a technique or confuse framework
coverage with security.

### Prioritization

Prioritize hypotheses by:

- business impact and data sensitivity;
- external or cross-tenant reachability;
- privileges required;
- likelihood and reliability;
- breadth of affected assets;
- detectability and recovery difficulty;
- known active exploitation when supported by evidence.

## 5. Web Application and API Assessment

Use the versioned OWASP Web Security Testing Guide and the application's threat
model. Test through approved accounts and data.

### Attack-surface mapping

Inventory:

- routes, methods, parameters, headers, cookies, and content types;
- browser, mobile, partner, internal, and administrative clients;
- REST, GraphQL, RPC, WebSocket, upload, callback, and webhook surfaces;
- static files, backups, debug endpoints, documentation, and source maps;
- authentication flows, recovery, registration, invitations, and federation;
- background jobs triggered by requests;
- server-side outbound connections;
- tenant and object identifiers.

Compare observed routes with source and gateway configuration. Hidden does not
mean authorized, and an undocumented route may still be business critical.

### Authentication

Assess:

- transport protection and secret handling;
- account enrollment, activation, recovery, and deprovisioning;
- session identifier entropy, rotation, expiry, revocation, and binding;
- MFA enrollment, recovery, step-up, and bypass paths;
- rate limits using agreed low-volume test accounts;
- remember-me and device-trust behavior;
- federation state, nonce, audience, issuer, redirect, and token validation;
- logout behavior across browser, API, and identity provider sessions.

Do not collect real user passwords or spray credentials unless explicitly
approved. Prefer configured test identities and control review.

### Authorization

Build a subject-resource-action matrix:

| Subject | Resource owner/tenant | Action | Expected result |
|---|---|---|---|
| Anonymous | Public | Read | Per policy |
| User A | User A | Read/update | Allowed subset |
| User A | User B | Read/update | Denied |
| Tenant A admin | Tenant A | Admin action | Allowed |
| Tenant A admin | Tenant B | Any | Denied |
| Support role | Customer | Sensitive action | Explicitly constrained and audited |

Test every meaningful action, not only page visibility. Include list, search,
export, bulk, nested resources, background jobs, and direct object references.
Verify authorization at the authoritative server-side boundary.

### Input and interpreter boundaries

Trace untrusted data into:

- SQL and non-relational queries;
- shell or process execution;
- templates and browser contexts;
- filesystem paths and archive extraction;
- LDAP, XML, XPath, regular expressions, and expression languages;
- serialization and object construction;
- redirects, headers, logs, and mail;
- server-side URL fetches;
- generative-model prompts and tool invocation.

Use non-destructive markers and controlled data to determine whether input
changes interpreter structure or crosses a trust boundary. Source review and
parameterized-query inspection can prove root cause with less risk than
extracting data.

### Browser security

Review:

- context-specific output encoding and unsafe DOM sinks;
- cross-site request forgery protections for state changes;
- cookie attributes and session storage;
- content security policy as defense in depth;
- cross-origin policy and credential behavior;
- frame embedding and clickjacking-sensitive workflows;
- postMessage origin and message validation;
- caching of sensitive responses.

Browser headers do not replace correct authorization and output handling.

### Files and content

Test filename, path, size, media-type, archive, storage, retrieval, and
execution policy. Use inert files. Verify that:

- names are generated or safely normalized;
- storage is outside executable locations;
- access checks apply on retrieval;
- archives have entry-count, total-size, path, nesting, and compression-ratio
  limits;
- active content is not served under an unsafe origin or content type;
- deletion and retention meet policy.

### SSRF and outbound access

Assess how the server resolves, redirects, authenticates to, and connects to
user-influenced destinations. Validate protections against loopback, private,
link-local, metadata, and disallowed schemes using an authorized controlled
endpoint. Do not probe unrelated internal systems to demonstrate impact.

### Business logic and concurrency

Model state transitions and invariants such as balance, inventory, entitlement,
approval separation, one-time tokens, and idempotency. Test:

- skipped or reordered workflow steps;
- replay and duplicate submission;
- quantity, sign, precision, and boundary values;
- stale authorization after role or ownership change;
- concurrent requests against single-use or limited resources;
- asynchronous jobs that trust stale request state.

Keep values small and synthetic. A race is confirmed by controlled invariant
violation, not by exhausting production capacity.

### API-specific concerns

- enforce schema, nesting, collection, and payload limits;
- apply authorization to each object in batch and graph responses;
- bound GraphQL depth, breadth, aliases, and resolver cost;
- prevent mass assignment of server-controlled fields;
- sign and replay-protect webhooks;
- distinguish authentication from tenant and object authorization;
- define pagination consistency and export limits;
- avoid exposing internal error or schema detail unnecessarily.

## 6. Network and Infrastructure Assessment

### Inventory and exposure

Reconcile approved inventory with:

- DNS and certificate names;
- internet and partner-facing addresses;
- load balancers, gateways, VPNs, and remote administration;
- listening services and protocol versions;
- management interfaces;
- internal segmentation zones;
- outbound paths and name resolution.

Use rate-limited discovery within exact target ranges. Confirm service identity
through multiple signals; banners and inferred versions can be wrong.

### Configuration review

Assess:

- unsupported operating systems, firmware, and services;
- default or unnecessary services;
- administrative interfaces exposed beyond management networks;
- plaintext or obsolete protocol configurations;
- TLS versions, certificate validation, trust stores, and key handling;
- anonymous or overly broad shares;
- host firewall and endpoint protection policy;
- centralized authentication and logging;
- backup, recovery, and immutable-copy protections.

A version match to a vulnerability database is a hypothesis. Confirm the
installed package, configuration, reachability, mitigations, and vendor
advisory before reporting exploitability.

### Segmentation and egress

Define an approved source-destination-port matrix. Test whether controls enforce
it from representative test hosts and identities. Include:

- user to server and server to server;
- production to management;
- tenant or environment separation;
- inbound and outbound paths;
- DNS and proxy enforcement;
- failure behavior when policy engines are unavailable.

Prove an unexpected path with a benign connection or canary service. Do not
enumerate or interact with systems beyond the approved destination.

### Network authentication and management

Review:

- shared versus individual administrative identity;
- MFA and privileged access workflows;
- key, certificate, and secret rotation;
- management-plane source restrictions;
- protocol downgrade and fallback;
- device configuration backup and change audit;
- time synchronization needed for authentication and evidence.

Wireless, telephony, industrial, or safety-related testing needs specialized
scope and impact controls beyond a general network assessment.

## 7. Cloud, Container, and CI/CD Assessment

Cloud security depends on identity, resource policy, control-plane APIs, and
provider boundaries. Confirm the provider's current penetration-testing policy
before testing.

### Cloud inventory

Map:

- organization, tenant, account, subscription, project, and folder hierarchy;
- regions and resource owners;
- human, workload, federated, and managed identities;
- role assignments and policy inheritance;
- virtual networks, private endpoints, gateways, and public addresses;
- storage, databases, queues, functions, clusters, and secret stores;
- logging, security monitoring, and organization guardrails.

### IAM analysis

For each principal, evaluate effective permissions, not only attached role
names. Look for:

- broad wildcards and resource-independent privileges;
- privilege-escalation chains through role assignment, delegation, policy
  editing, instance profiles, function deployment, or secret access;
- trust policies that accept unintended accounts, subjects, or claims;
- long-lived access keys and unmanaged service accounts;
- inherited grants and group nesting;
- stale identities and incomplete deprovisioning;
- missing separation between deployment and runtime privileges.

Validate a suspected escalation with a dedicated test principal and harmless
canary resource. Do not assume a more privileged production role.

### Storage and data services

Check public and cross-account policy, anonymous behavior, encryption and key
access, snapshots, backups, replication, logging, retention, and tenant
separation. Listing metadata can itself be sensitive. Stop before bulk content
access; use a canary object or owner-approved sample.

### Workload metadata and secrets

Assess whether workloads can reach metadata services, which identity they
receive, and whether request protections are enforced. Review secrets in:

- images and layers;
- environment variables;
- instance bootstrap data;
- source and CI logs;
- deployment manifests;
- build caches and artifacts.

Do not copy live secrets into the report. Record location, scope, a fingerprint
or redacted fragment, and rotation status; coordinate immediate rotation when
exposure is confirmed.

### Containers and orchestration

Review:

- image provenance, signatures, update policy, and known vulnerabilities;
- runtime user, capabilities, seccomp or equivalent policy, and filesystem
  writability;
- host mounts, privileged mode, device access, and container socket exposure;
- namespace and workload identity;
- network policy and default behavior;
- admission controls and policy exceptions;
- secret delivery, audit logs, and control-plane access;
- resource limits and noisy-neighbor exposure.

Test escape or node impact only in an expressly approved isolated environment.
Production review can usually establish dangerous configuration without
crossing the boundary.

### CI/CD and software supply chain

Trace who can:

- modify source, workflows, dependencies, build images, and release metadata;
- approve and publish artifacts;
- access signing keys and deployment credentials;
- bypass branch, review, or environment controls;
- retrieve untrusted pull-request artifacts or caches.

Assess artifact provenance, dependency pinning, build isolation, secret scope,
runner trust, review requirements, and separation of build from deployment.
Use a harmless canary pipeline or repository where active validation is needed.

## 8. Identity and Privileged Access Assessment

Identity is a control plane across applications, cloud, endpoints, and network.

### Lifecycle

Test joiner, mover, leaver, contractor, and emergency-access processes:

- authoritative identity source;
- timely provisioning and removal;
- role and group approval;
- dormant account detection;
- service account ownership and expiry;
- periodic access review;
- break-glass control and audit.

### Authentication and MFA

Review accepted factors, enrollment, recovery, device replacement, fallback,
step-up triggers, session lifetime, and revocation. Determine whether a weaker
recovery path negates the primary MFA control. Use dedicated accounts; do not
prompt or fatigue real users.

### Federation and tokens

Validate:

- issuer, audience, signature, algorithm, expiry, and not-before checks;
- state and nonce binding;
- redirect and reply URL restrictions;
- subject mapping and tenant restrictions;
- group and role claim size or truncation behavior;
- refresh-token rotation and revocation;
- logout and downstream session invalidation;
- signing-key rotation and cache behavior.

Never treat token decoding as signature validation. Do not place live bearer
tokens in evidence.

### Directory and enterprise identity

Review effective delegation, privileged group nesting, service identities,
legacy authentication, certificate-based identity, machine trust, and
administrative tier separation. Validate suspected privilege paths in a lab or
with purpose-created test objects unless production modification is explicitly
approved.

## 9. Exploitability Validation With Minimum Harm

A scanner result, code pattern, or version match is not automatically a
confirmed vulnerability. Validation should answer:

- Is the weakness present in this deployed configuration?
- Is the vulnerable path reachable by the stated actor?
- Which preconditions and privileges are required?
- Which security boundary can be crossed?
- What is the smallest safe evidence of impact?
- Do compensating controls prevent or detect the path?

### Evidence ladder

Stop at the lowest sufficient level:

1. **Configuration evidence**: authoritative setting or source path proves the
   control is absent.
2. **Behavioral evidence**: a benign request produces behavior inconsistent
   with the intended control.
3. **Canary impact**: a synthetic record, file, role, or callback demonstrates
   read, write, execution, or privilege impact.
4. **Chained impact**: multiple confirmed findings demonstrate a business path,
   only when separately approved and needed for prioritization.

Do not access more records, maintain persistence, dump secrets, or pivot farther
once the claim is proven.

### Safe proof patterns

- Read or alter a tester-owned canary object rather than customer data.
- Use a unique inert marker rather than executing an operating-system command.
- Demonstrate an unexpected network path against a controlled listener.
- Request a harmless permission on a dedicated cloud resource.
- Show source and runtime evidence together when active exploitation adds risk
  but no useful certainty.
- For data exposure, record schema, count metadata, or one approved synthetic
  record rather than exporting the dataset.

### Handling apparent zero-days

If behavior appears novel:

- stop broad testing and reproduce on the minimum approved target;
- preserve exact version and configuration evidence;
- rule out known advisories and deployment-specific mistakes;
- notify the owner through the emergency path;
- coordinate vendor disclosure through an agreed channel;
- do not publish details or test unrelated installations;
- prioritize containment and detection while root cause is investigated.

## 10. Evidence and Chain of Custody

Every confirmed finding should have:

- finding identifier and title;
- tester, timestamp, time zone, source, target, and account;
- environment and exact component version or revision;
- preconditions and role;
- sanitized request/action and response/observation;
- affected object and tenant scope;
- expected versus actual control behavior;
- minimal impact evidence;
- links to relevant logs, code, configuration, or screenshots;
- tool name/version and material settings;
- cleanup performed and remaining artifacts.

### Evidence handling

- Collect the minimum needed.
- Encrypt in transit and at rest.
- Restrict access by finding sensitivity.
- Hash immutable evidence when integrity matters.
- Keep originals read-only and analyze working copies.
- Redact credentials, tokens, personal data, and unrelated tenant data.
- Record transfers and transformations.
- Apply the authorized retention and secure-deletion schedule.

Screenshots alone are weak evidence when text, logs, or structured exports are
available. A tool report without manual validation is not proof.

### Reproducibility

Steps must be precise enough for the owner to reproduce without exposing an
unsafe reusable exploit package. Include identifiers and conditions, but
replace secrets and personal data with placeholders. State whether the result
is reliable, timing-sensitive, configuration-dependent, or intermittent.

## 11. Risk Rating

Separate technical severity from business priority.

Technical scoring may consider:

- attack vector and complexity;
- privileges and user interaction;
- confidentiality, integrity, and availability impact;
- scope or boundary change.

Business context includes:

- asset criticality and data sensitivity;
- affected population and tenant breadth;
- fraud, safety, legal, and operational impact;
- internet exposure and exploit maturity;
- detection, containment, and recovery;
- existing compensating controls.

Use CVSS consistently when required, preferably the current version, and show
the vector. Do not let a numeric score replace a plain-language impact
explanation. Record uncertainty instead of inventing precision.

## 12. Reporting

### Executive view

Explain:

- assessment scope and limitations;
- major attack paths and business impact;
- systemic themes and concentration of risk;
- immediate containment actions;
- prioritized remediation program;
- residual uncertainty and untested areas.

### Finding format

Each finding should include:

1. concise title and status;
2. affected assets and versions;
3. actor and prerequisites;
4. expected security boundary;
5. observed behavior and minimal evidence;
6. technical and business impact;
7. root cause;
8. severity rationale and scoring vector;
9. specific remediation and defense in depth;
10. regression test and retest plan;
11. references to CWE, vendor guidance, or standards.

Do not inflate counts by reporting every instance of one systemic root cause as
an unrelated finding. Preserve affected-location detail in an appendix.

### Distinguish certainty

- **Confirmed**: reproduced with sufficient evidence.
- **Likely**: strong evidence but active confirmation was unsafe or prohibited.
- **Unverified**: hypothesis requiring owner action or additional scope.
- **Informational**: hardening or visibility opportunity without a demonstrated
  security-boundary failure.

## 13. Remediation Engineering

Good remediation removes the root cause and creates durable evidence.

### Remediation hierarchy

1. Remove unnecessary exposure or functionality.
2. Enforce the missing invariant at the authoritative boundary.
3. Reduce privileges and isolate sensitive resources.
4. Use safe framework or protocol mechanisms.
5. Add defense-in-depth validation, rate limits, or containment.
6. Improve detection and response.
7. Apply temporary compensating controls with an owner and expiry.

Examples:

- Broken object authorization: centralize server-side subject-resource-action
  policy, scope data queries by tenant/owner, and add negative matrix tests.
- Injection: preserve data/code separation with parameterized APIs, validate
  identifiers from an allowlist, and add sink-focused regression tests.
- Excess cloud permission: replace broad grants with task-specific roles,
  constrain trust and resources, and continuously analyze effective access.
- Segmentation gap: define an explicit communication matrix, enforce at more
  than one relevant layer, and continuously test canary paths.
- Secret exposure: revoke and rotate first, remove the source, reduce scope and
  lifetime, then add scanning and log controls.

“Sanitize input,” “patch the server,” or “follow best practices” is not a
sufficient fix description. Name the control, enforcement point, owner, and
verification.

### Retest

A retest should:

- reproduce the original test and observe denial or safe handling;
- test equivalent encodings, routes, roles, and object types within scope;
- verify no fallback or alternate path remains;
- inspect the code/configuration root cause when available;
- confirm logs and alerts;
- ensure the fix does not break authorized behavior;
- document residual exposure and accepted risk.

Close a finding only when agreed evidence exists. A version upgrade is not
proof if vulnerable configuration or behavior remains.

## 14. Assessment Quality Checklist

- Written authorization is current and exact.
- Tool targets and rates enforce the rules of engagement.
- Threat hypotheses connect assets, actors, boundaries, and impact.
- Active testing uses the least harmful sufficient technique.
- Findings distinguish detection, presence, reachability, and exploitability.
- Evidence is reproducible, minimal, sanitized, and protected.
- Severity explains business context and uncertainty.
- Remediation addresses root cause and includes a regression test.
- Cleanup, credential rotation, evidence retention, and retest are complete.
- Untested and out-of-scope areas are explicit.

## Stable Primary References

- **[US government testing methodology]** NIST SP 800-115,
  [Technical Guide to Information Security Testing and
  Assessment](https://csrc.nist.gov/pubs/sp/800/115/final).
- **[US government risk assessment]** NIST SP 800-30 Rev. 1,
  [Guide for Conducting Risk
  Assessments](https://csrc.nist.gov/pubs/sp/800/30/r1/final).
- **[US government security framework]** NIST,
  [Cybersecurity Framework 2.0](https://www.nist.gov/cyberframework).
- **[Versioned web testing guide]** OWASP,
  [Web Security Testing Guide
  v4.2](https://owasp.org/www-project-web-security-testing-guide/v42/).
- **[Application control requirements]** OWASP,
  [Application Security Verification
  Standard](https://owasp.org/www-project-application-security-verification-standard/).
- **[Threat knowledge base]** MITRE,
  [ATT&CK](https://attack.mitre.org/).
- **[Weakness taxonomy]** MITRE,
  [Common Weakness Enumeration](https://cwe.mitre.org/).
- **[Vulnerability scoring standard]** FIRST,
  [Common Vulnerability Scoring System
  v4.0](https://www.first.org/cvss/v4.0/).
- **[Cloud provider rules]** AWS,
  [Penetration Testing](https://aws.amazon.com/security/penetration-testing/).
- **[Cloud provider rules]** Microsoft,
  [Penetration Testing Rules of
  Engagement](https://www.microsoft.com/en-us/msrc/pentest-rules-of-engagement).
- **[Cloud provider rules]** Google Cloud,
  [Cloud penetration testing
  guidance](https://support.google.com/cloud/answer/6262505).
- **[Supply-chain framework]** NIST SP 800-218,
  [Secure Software Development
  Framework](https://csrc.nist.gov/pubs/sp/800/218/final).

Provider rules and legal requirements can change. Verify current policies for
the actual service and jurisdiction before each assessment.
