# The 60–90 second AfterThought demo

Use the **Graph Runtime** page for the whole demonstration. The three visible sections connect the inputs, graph evolution, and measured results.

## Prepare in the frontend

Start the local server with `make demo` and open <http://127.0.0.1:8080/graphs.html>. If an older server is running, stop it with Ctrl+C first. PDF extraction requires Poppler. Allow time for model authoring and evaluation before presenting.

1. Enter your name and click **Start demo**. The page loads 1,000 synthetic claims and the fictional company-policy PDF, then starts authoring. Follow the visible progress and expandable model activity.
2. Review the initial graph and evaluation. Click **Approve and activate**. Your name is already filled in.
3. Watch the timeline update automatically as ordinary claims and the controlled emerging-pattern batch run. The serving counters show the actual model-call and token counts.
4. Review the cited policy proposal. Enter your assessment and a note such as “Verify original supporting evidence before any claim consequence,” then click **Approve policy amendment**.
5. The agent proposes a revision to the serving output node. Wait for evaluation. Inspect **Measured before and after** and **Inspect the changed node**.
6. Leave this graph revision awaiting activation for your presentation. The next approval is your live action.

The demo uses generated labels based on repair-estimate ratio. The emerging batch increases evidence reuse from 0–2 to 8–12. A policy-driven revision can improve investigation reasons while retaining classification metrics. Use the displayed measurements when describing results.

## Spoken walkthrough

| Time | On screen | Say |
| --- | --- | --- |
| 0–12 s | Dataset & knowledge | “We gave AfterThought a claims dataset and our company policy. These are synthetic inputs for today’s demo.” |
| 12–25 s | Workflow | “The agent authored and evaluated this reusable graph. Once approved, it processed a thousand claims with zero serving-model calls.” |
| 25–42 s | Learning & results | “A new pattern appeared in incoming evidence. The agent proposed policy checks, and after my review, it authored this change to one workflow node.” |
| 42–55 s | Changed node and comparison | “Here is the actual code change and its before-and-after evaluation. We can inspect exactly what changed and whether performance held up.” |
| 55–75 s | Approve and activate | “I decide when the revision goes live. The same batch now runs through the approved graph, and we can compare its recommendations.” |

Rehearse the final batch once to measure its runtime. For a strict one-minute presentation, complete activation beforehand and show the recorded before/after results. Identify them as a completed run. Cold model authoring can take several minutes.

## Controls and recovery

- **Demo options → Show technical workspace** reveals manual CSV upload, full graphs, model/tool traces, and one-node tuning.
- Keep the page open for automatic handoffs. Durable server jobs continue if it closes. Reopening the same browser/origin resumes the saved demo.
- **Retry preparation** reuses stable build and batch IDs after uncertain admissions. A terminal failed evaluation needs inspection in the technical workspace.
- **Start another demo** creates a fresh scope. Existing server evidence remains available.
- The guided cycle requests review after its two demonstration batches. Scheduled Auto-review continues to use 50 completed batches.

## Six-minute presentation

Spend 45 seconds on the data-science bottleneck, 45 seconds on the build/serve/improve cycle, 75 seconds on this demo, 60 seconds on Temporal and Hindsight, 45 seconds on measured results, and 30 seconds on the next milestone. Reserve the remaining minute for transitions and questions.
