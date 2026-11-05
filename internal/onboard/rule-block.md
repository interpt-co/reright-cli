<!-- reright:start -->
## reright: rewrite text people read

Before you use any text a person will read (emails, messages, posts, documents, commit messages, PR and issue text, comments), call the reright tool `rewrite` with the text, its `kind` (`email`, `message`, `post`, `document`, `code` or `other`), where it goes (`target`) and a line of `context`. For an email, a commit or a PR, the first line is the subject or title, then a blank line, then the body.

Send only free text, the words a person writes. Text with nothing to rewrite, such as a URL, a number, a version, an id, a file or branch name, a name or code, is used as it is without calling `rewrite`. When the words go into a template (a PR or issue template, a form, a fixed footer or signature), put just those words in `text` and the whole template in `template`, with `{{text}}` where the words go. reright rewrites the words and returns the filled template.

If the status is `pending`, call `rewrite_result` with the `id` until it is not. If it is `rejected`, stop and tell the user. When it is `approved`, compare `final_text` with what you wrote. If the rewrite changed the meaning (a fact, a name, a number, a date, a yes or no, a promise), call `keep_original` with the `id` and what changed, and use the text it returns. Otherwise use `final_text` exactly, character for character. Never edit it yourself.

Hooks block `git commit`, `gh` PR and issue text and Gmail sends whose text did not come back from reright.
<!-- reright:end -->
