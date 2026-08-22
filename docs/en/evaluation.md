# Replay evaluation (wen eval)

[← Back to README](../../README.en.md)　·　[中文](../evaluation.md)　·　English


You have just adjusted a prompt — is the character better now, or worse? `wen eval` turns that question from "chat a bit more and see how it feels" into a script you can run again:

```bash
wen eval docs/eval/example.json              # report goes to stdout
wen eval docs/eval/example.json -o report.md # written to a file
```

A script is a single JSON file. `name` names the report; `turns` lists the steps in order — `{"say": "what you say"}` is one turn of conversation, `{"compact": true}` forces a compaction at that point (to see whether the character survives it), and `{"gap_hours": 8}` only notes "this much time passed here" in the report (it does not change the system clock, and the model never sees it). The optional `judge_points` adds a few things for the judging pass to pay attention to. See [`docs/eval/example.json`](../eval/example.json) for an example.

It runs with **the same configuration and plugins as `wen serve`** — the same config.yaml, models.json and plugins.state.json, so the character sheet, the sample lines and the natural-expression rules are all in effect — with two differences. Sessions and plugin data all go to a temporary directory that is deleted when the run finishes, so the memory store, the mood and every other piece of state start blank and nothing real is touched. And the chat-channel and background-task plugins (heartbeat, scheduler, QQ / WeChat / Feishu / Telegram) do not start: they would connect to real platforms, or interject in the evaluation session. You can run it while the service is up; the two do not interfere.

The report is Markdown: the script name, the number of turns, the number of compactions (script-requested and automatically triggered counted separately), and per turn the character count, sentence count, share of acted-out narration and assistant-speak hits (the same rules `style_watch` uses). Then the current model does one judging pass, scoring "does it read as the same person", "is the tone consistent (especially across a compaction)" and "are the relationship and the forms of address continuous" from 1 to 5 with a sentence of reasoning each. Every reply is appended verbatim at the end.

A few limits worth keeping in mind. The scores come from a model too, so the same script run twice will not score identically — **read them as a trend, not as absolute values**; the meaningful comparison is one run before a prompt change and one after. Assistant-speak detection is a regex heuristic, and a hit does not mean the character broke. The memory store is empty, so anything that depends on long-term memory cannot be measured. And `gap_hours` does not make the model actually feel time pass. If a turn fails, the report is still produced with that turn marked, but the exit code is 1.
