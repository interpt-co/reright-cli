<!-- reright:start -->
## reright: approval for text people read

Every piece of text a person will read goes through reright before you use it: emails, messages to clients and colleagues, posts, documents, commit messages, PR and issue text, and comments. That includes drafts you would otherwise show the user in chat for approval. Write the text plainly, the way the user would, without padding.

1. Call `submit_for_review` (reright MCP server) with the kind, the exact text, the target, `agent`, `run` and a context block.
   - `kind` is `email`, `message` (chat, replies, direct messages), `post` (anything public), `document`, `code` (commits, PRs, issues, code comments) or `other`. Say where the text goes in `target`: a recipient, a channel, a repo and branch, a file and line.
   - For an email, a commit or a PR, the first line is the subject or title, then a blank line, then the body.
   - `agent` is your short name or the tool you run in, such as `Claude Code`. `run` names the loop, job, session or subagent, such as `nightly-digest` or `fix-rounding`. The reviewer often has several of these waiting, and the queue groups by them, so use the same spelling every time.
   - Write `context` as markdown in plain language, with these short headings: `## What this is` (one line naming the text and where it goes), `## Why` (the reason, and the ticket or thread it answers), `## Who reads it` (the audience and what they already know), `## What changed or history` (the change in a few bullets, or the earlier messages). Leave out a heading that has nothing to say. The reviewer reads it beside the text to check the text against it.
2. Give the user the review link once, then call `wait_for_review` with the id until the status is no longer pending.
3. Approved: use `final_text` exactly, character for character. Do not reformat, rewrap or add to it.
4. Rejected: stop the task. Do not retry, rephrase or look for another way to send it. Tell the user it was rejected and wait for their instructions.

Hooks block `git commit`, `gh` PR and issue text, Gmail sends and browser `insertText` whose text reright has not approved. Where a skill says to show the user the exact text and wait for their go, reright approval is that go.
<!-- reright:end -->
