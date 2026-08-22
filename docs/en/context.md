# How the context is organized

[← Back to README](../../README.en.md)　·　[中文](../context.md)　·　English


The context sent with one request has two layers:

```
system        [system environment] + [tools and replies] + [history and time] + each plugin's
              fixed prompt + the system prompt you configured        ← byte-for-byte identical every turn
history       past messages; a line is inserted where the date turns over ("the conversation
              below is from Thursday 2026-08-20") and where a long gap sits ("about 14 hours
              passed since the previous message")
<this turn>   the current time, how long since the previous message, and the fragment each
              plugin generates for this turn (mood / weather / memory index …)
input         what you just said
```

**The current time is not in the system message; it comes after the history.** That single decision solves two problems at once:

- **The model's sense of "now".** Placed at the top of the system message, the time sits thousands of tokens away from where generation happens, while the moments mentioned in the conversation are narrative fact and right there in front of it — a dozen turns in, the model treats the timestamp from last time as the present and goes on playing out a late night in the middle of the afternoon. Putting it after the history, and saying plainly that it wins over anything the history claims, is what holds. For the same reason a gap marker is inserted where a long silence sits: the timestamps were always on the messages, they were simply never sent to the model, so those fourteen hours did not exist in the context at all.
- **Prompt caching.** Every cache matches on an exact prefix, so a timestamp that changes every turn inside the system message means the whole system message plus history can never hit. Providers like DeepSeek that cache automatically server-side offer no switch to adjust; the only handle you have is keeping the prefix still.

`[tools and replies]` is a short set of rules for the model: a tool result is the receipt for its own call, not something you said; answer once per turn; do not resubmit what has already been done. Without it, a model asked to generate again after a tool call will **invent a line for you and then answer it**, or resubmit the same tool over and over. There is a hard guard behind the prompt as well: within one turn, a call with the same name and the same arguments is not executed again, no tool runs more than six times, and three blocked calls end the turn.

`[history and time]` is a second set of rules, aimed at a mismatch that is very hard to notice on your own. One evening the character says "I'll bring you two dishes tomorrow." That sentence stays in the context from then on, worded "tomorrow" forever. The next day you take them, but nothing in the conversation records that it happened, and nothing says which day that "tomorrow" referred to — so that evening it says the line again, and its new "tomorrow" becomes the freshest evidence for the day after, extending itself indefinitely. Three rules therefore live together: "today / tomorrow / yesterday" in the body of a message is read against the **date line** the message sits under, not against now; an intention the character voiced does not mean it still stands, nor that it has not been carried out; and `<this turn>` is the authority on the state of such things.

The date line is that "the conversation below is from Thursday 2026-08-20" in the history. With gap markers alone, locating which day some "tomorrow" meant would require the model to add up every gap from that message to the end — an arithmetic chain it does not do reliably; the date line turns it into a single lookup. The marker is attached to your message where possible. In a turn the character opens by itself (in a heartbeat turn, the injected prompt is sent once and is gone from the context afterwards) there is no message of yours to attach it to, so a standalone line is inserted instead — precisely those spontaneous lines had no time signal at all before, and "you told me yesterday you'd do X tomorrow" is usually exactly how such a thing gets said. All of these markers are added only to the copy that goes out; nothing is written to disk, and the cache prefix is unaffected (they are derived from timestamps the messages already carry and never change once generated).

**Compaction summaries are required to write absolute dates too.** A summary is never trimmed, so every sentence in it reappears, worded identically, at the very front of every later turn — an entry saying "promised to bring dishes tomorrow" is welded there, showing up afresh every day. The compactor now receives the date lines, and is asked to convert relative wording into concrete dates and to record, for anything still outstanding, which day it was said and which day it is due.

`/status` reports how much of the last turn came from cache, which is how you confirm it is actually working.
