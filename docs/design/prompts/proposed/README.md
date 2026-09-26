# Proposed prompt texts (not in the code)

These are the round-5 prompts lens's proposed rewrites, kept beside
`docs/design/prompts.md` so the proposal the owner reads is the text the
gated PR would ship. None of them is sent to a model today. They await the
owner's live eval baseline (`LIVE_LLM=1 LLM_API_KEY=… go test
./internal/provider -run TestLiveEval -v -count=3` on the current prompts),
after which each lands only with a `-count=3` pass on the new text recorded
in its PR.

| File | Item | Replaces | Gate |
|---|---|---|---|
| `route.system.txt`, `route.user.sample.txt` | PR-2 (routing rewrite: sections, dead rules removed, ordinals in place of note ids, the title rule stated as `pipeline.preferExistingTitle` enforces it, the language line) | `routing.systemPrompt` and `routing.UserPrompt`, `parseRouteDecision` reading `note` as a number | `TestLiveEval/route -count=3`; PR-D1 (tags) and PR-D2 (200 candidates) ride on it if answered first |
| `items.system.txt` | PR-4 (checklist items prompt tightened, 643 → 480 tokens, same rules and examples, the "as spoken" exception made explicit) | `cleanup.itemsSystemPrompt` | `TestLiveEval/items -count=3` |
| `cleanup.system.txt` | PR-D5 (per-capture cleanup collapsed to one template; Polished stays as the whole-note view) | `cleanup.faithfulSystemPrompt` / `polishedSystemPrompt` | owner decision, then `TestLiveEval/cleanup -count=3` |

The shared rules (`llm.DataRule`, `LanguageRule`, `NoInventionRule`) and the
whole-note template with the note's language landed in round 5 wave 1
(PR-1, PR-5) and are not repeated here. Measurements behind the token
figures: `docs/reviews/2026-09-26/r5/prompt-measurements.md`.
