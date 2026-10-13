# Action control and instruction-conflict incidents

Action Control is an independent component with a standalone deterministic Git
commit-message validator. It does not install hooks, execute commits or enforce
restrictions on agents. Its broader policy/approval/execution direction remains
future work, separate from Continuity's no-execution MVP.

## Implemented validator

The command accepts an explicit versioned JSON policy and selected message file.
It validates the policy effect and prohibited keys in Git's final trailer paragraph,
preserves diagnostic line numbers around template comments, and verifies a supplied
canonical policy hash. Policy and message inputs are bounded at 1 MiB each.
A valid message, policy violation and invalid input have distinct exit statuses.
A note requirement is reported, but the message check cannot prove a post-commit
note will be attached.

The policy shape originated with the retired Continuity ActionPolicy interface;
the standalone validator still accepts an externally supplied policy.
Continuity no longer offers an effective-policy compiler, restriction/incident
record family or ActionPolicy generator.
See the [component guide](../components/action-control/README.md).

## Instruction-conflict diagnosis

Separate forgotten guidance, an observed violation and an agent-reported conflict.
A claim of platform origin remains a claim until independently established.
Inspect supported client configuration and its effective scope before assuming a
behavior is an unavoidable requirement.

Retain the requested action, applicable instruction, observed result, attribution,
source and resolution in current narratives/decisions/work items.
Do not record hidden reasoning. A reminder or successfully recalled instruction
does not prove compliance at the action boundary.

A future incident/restriction model would require explicit scope, lifecycle,
correction and clearance rules. It is not an installed enforcement mechanism.
Recorded restrictions and externally prevented capabilities must be labeled
separately.

## Future execution boundary

A reviewed commit flow would bind approval to repository identity, HEAD, staged
tree, exact message and policy version, reject changed inputs and record partial
success if an ancillary note fails. Installing a hook or executing an approved
commit remains separately scoped work.

Local hooks and wrappers are bypassable when an agent retains direct Git writes.
Strong enforcement requires a boundary the agent cannot bypass. An execution
broker would resolve command/environment/network/isolation policy and return
attributable receipts; it must not turn Continuity's MCP server into a generic shell.

Prefer supported configuration and measured need before adding an executor.
See [agent-host security](agent-host-security.md) and
[decision history](decision-history.md#agent-attribution-inspection-and-replay).
