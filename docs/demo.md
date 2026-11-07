# CSV-to-graph demo

For a short hackathon presentation, follow the [60–90 second script](demo-90-seconds.md), using the frontend learning view and timed narration.

Run `make demo` and open <http://127.0.0.1:8080/graphs.html>. First startup builds
the generic numerical environment; `.env` supplies the model and Hindsight.

For the manual workspace, open **Demo options → Show technical workspace**.
For a synthetic demonstration, click **Prepare 5,000-row demo** in the evaluation
dataset panel. It selects a generated labelled CSV, fills the dataset policy and
an empty authoring prompt, and selects a matching label-free inference CSV.
Review the generated label meaning and explicitly confirm it before uploading.
This preparation starts no model call and approves no release. After a page
reload, **Prepare 5,000-row demo batch** in the batch panel selects the inference
CSV again. Both files use `signal`, `context`, and `noise`; use an approved graph
trained for those features. No Python command or manual file creation is needed.

1. Upload a labelled CSV. Declare the target, positive label, meaning, permitted
   features, and precision/recall/false-positive thresholds. Verify label provenance
   externally and mark synthetic data explicitly. Types and train/development/audit
   membership are frozen before authoring.
2. Describe the objective and start bootstrap. The agent starts without business
   nodes, reads the staged interface/profile, and authors `candidate.json` with
   Python fit/serve nodes. Follow its session for actual tool calls and tests.
3. Inspect each node's pinned source, configuration, dependencies, image, and
   revision. The backend executes the fit DAG and measures the frozen serve DAG
   on held-out data. Failed execution candidates remain inspectable and tunable.
4. An eligible candidate waits for a human decision. Enter your reviewer name and
   click **Approve and activate**. The local frontend records this explicit decision
   after checking evaluation and the expected active release. Every revision
   requires a separate human decision.
5. Upload an inference CSV for an approved release. Inspect the node trace and
   zero-model counters, then download all predictions and reasons. Each node runs
   on a batch; there is no LLM call per record.
6. Select one node, choose **Prepare revision for this node**, and request a
   grounded improvement. Only that node's source/config may change. Serving-node
   edits reuse the fitted artifact; fit-node edits retrain. The whole candidate is
   reevaluated, development regressions fail, and activation waits for approval.
   Existing runs retain their pinned versions.

Use synthetic labelled data to demonstrate mechanics until real ground truth is
available. The Kaggle extract has no verified fraud label; `CLAIM_STATUS` is
excluded. Repeatedly examining audit metrics consumes independent holdout evidence;
obtain fresh final audit data and use entity/time splits where appropriate.
Predictions are recommendations, not settlement actions.

The general agent workspace remains at `/`; the fictional claims replay remains
at `/claims.html`. Its browser decisions and rules do not seed live graph nodes.

`make test` runs Go and browser-module checks. `make integration` uses real Docker,
Postgres, and Temporal with mock providers, covering 5,000-row serving, private
splits, explicit approval fixtures, rejected/stale approvals, node revisions,
zero model calls, bounded outputs, commit recovery, and history replay.
