<!-- reright:start -->
## reright: rewrite text other people read

Before you use any text, ask yourself: will anyone besides the user read it?

- Yes: emails, messages to other people, posts, PR, issue and review text, comments on shared platforms, documents sent to or shared with others, drafts the user will send to others, and commit messages in repos other people work in or read. Call the reright tool `rewrite` first.
- No: your replies to the user, notes, plans and docs for the user's own use, code comments, internal tools, scratch files, config, logs, and commit messages in a repo only the user works on. Skip reright.
- Not sure: ask the user once rather than guessing. If you can't tell whether a repo is shared, commit as usual: the hook blocks the commit in a shared repo, and then you send the message through `rewrite`.

Call `rewrite` with the text, its `kind` (`email`, `message`, `post`, `document`, `code` or `other`), where it goes (`target`) and a line of `context`. For an email, a commit or a PR, the first line is the subject or title, then a blank line, then the body.

Send only free text, the words a person writes. Text with nothing to rewrite, such as a URL, a number, a version, an id, a file or branch name, a name or code, is used as it is without calling `rewrite`. When the words go into a template (a PR or issue template, a form, a fixed footer or signature), put just those words in `text` and the whole template in `template`, with `{{text}}` where the words go. reright rewrites the words and returns the filled template.

If the status is `pending`, call `rewrite_result` with the `id` until it is not. If it is `rejected`, stop and tell the user. When it is `approved`, compare `final_text` with what you wrote. If the rewrite changed the meaning (a fact, a name, a number, a date, a yes or no, a promise), call `keep_original` with the `id` and what changed, and use the text it returns. Otherwise use `final_text` exactly, character for character. Never edit it yourself.

Hooks block Gmail sends, `gh` PR, issue and release text, and commit and tag messages in shared repos when the text did not come back from reright. A repo counts as shared when it has a remote on a hosting service and either commits by other people in its recent history or a public remote. The user can decide for a repo with `git config reright.commits always` or `never`.
<!-- reright:end -->
