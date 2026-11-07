# Demo walkthrough

Run `make demo` from the repository root, then open <http://127.0.0.1:8080>.
The existing `.env` supplies the authoring model and Hindsight configuration.

1. Click **Use a small demo task**, choose a learning scope, and start the agent.
   Use the default 12-turn budget. This task uses synthetic inputs and requires no
   dataset credentials or sandbox network access.
2. Follow the session status, real tool trace, token usage, and generated files.
   Refresh once while it runs to demonstrate reconnection to the same Temporal
   execution. Expand the Bash test output to verify execution evidence.
3. Open `graph.proposal.json` for the candidate preview, then inspect its source
   and test files. An unresolved image reference means it is not a runnable release.
4. Submit feedback that cites a concrete test or observed failure. The source
   journal is immediate; Hindsight retention is asynchronous. Recall the feedback
   after processing and inspect source attribution and supporting facts.
5. Reuse the same learning scope for another authoring task. The new session gets
   a fresh workspace with prior feedback and recalled evidence. Code sharing,
   evaluation, release promotion, and automatic graph changes remain future work.

The separate **Sample claims** page is an illustrative replay with fictional
policy. Its outcomes and simulated failures are not live agent results or fraud
labels. Passing synthetic tests proves only the assertions those tests cover.

For a real dataset request, enter its URL and objective. Enable sandbox
networking only if downloading is needed, provide authorized dataset access, and
require verified labels before reporting predictive accuracy. Never put source
PII or credentials in prompts or demo screenshots.

If the UI shows unavailable services, check `docker compose ps` and the `make demo`
terminal. `make test` runs Go and browser-module checks; `make integration` exercises
the HTTP authoring/feedback loop with real infrastructure and mock providers.
