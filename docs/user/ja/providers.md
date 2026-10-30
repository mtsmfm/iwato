# プロバイダー (認証情報)

[English](../providers.md)

プロバイダーは、認証情報 (トークン、API キー、OAuth の許可) を OpenShell のゲートウェイに預けるものです。プロバイダーを付けたサンドボックスのプログラムには、環境変数に置き換え用の値 (プレースホルダー) だけが渡り、リクエストがプロバイダーの宛先に届くときに OpenShell のプロキシが本物の値に置き換えます。Iwato は値を二度と表示しません。

## 追加と付け外し

サイドバーの「プロバイダー」を開き、種類を選んで名前 (あとから変えられません。例: `github-personal`) を付け、値を入れます。種類ごとに、設定される変数と値の送り先が表示されます。

プロバイダーは作成フォームで付けるか、あとからサンドボックスのメニューの「プロバイダー」で付け外しします。動いているサンドボックスでの付け外しは再起動で反映されます。それまでは「再起動待ち」、サンドボックスが変更を取り込むまでは「反映待ち」と表示されます。プロセスが動いている間に付けたプロバイダーは、その後に起動したプロセスにだけ届きます。Iwato の種類のプロバイダーを付けると、その規則がサンドボックスのポリシーに加わります (下記)。すでにある規則と食い違うときは、その規則を示して直し方を選べます。

Iwato のプロバイダーの種類:

| 種類 | 変数 | 説明 |
|---|---|---|
| 「Claude Code (Claude でログイン)」 | `CLAUDE_CODE_OAUTH_TOKEN` | Claude のアカウントでログイン (下記)。api.anthropic.com に送ります |
| 「Grok Build (xAI のサブスクリプション)」 | `XAI_OAUTH_TOKEN` | xAI のアカウントでログイン (下記) |
| OpenRouter | `OPENROUTER_API_KEY` | openrouter.ai/api |
| 「Atlassian (MCP)」 | `ATLASSIAN_ACCESS_TOKEN` | OAuth (下記)。送れるのは `claude` だけで、送り先は mcp.atlassian.com/v2/mcp |
| GitHub | `GH_TOKEN`、`GITHUB_TOKEN` | `gh` と HTTPS の git が使います。読み取りと clone・fetch。書き込みと push は提案の承認で |
| Hugging Face | `HF_TOKEN`、`HUGGING_FACE_HUB_TOKEN` | ダウンロードだけ。アップロードはポリシーの提案の承認で。ファイルの CDN (`*.hf.co` など) には値を送らないので、その提案は読み取りで承認します |
| Civitai | `CIVITAI_API_TOKEN`、`CIVITAI_TOKEN` | 読み取りとダウンロードだけ。生成 (Buzz を使う) は提案の承認で。ダウンロードの転送先には値を送らないので、その提案は読み取りで承認します |
| Comfy Cloud | `COMFY_CLOUD_API_KEY`、`COMFY_API_KEY` | ワークフローの実行 (クレジットを使う) もできます |
| Vast.ai | `VAST_API_KEY` | 読み取りとオファーの検索だけ。インスタンスの作成は提案の承認で |
| RunPod | `RUNPOD_API_KEY` | 読み取りだけ (api.runpod.io、rest.runpod.io、api.runpod.ai、hapi.runpod.net)。Pod の作成・起動とサーバーレスの実行は提案の承認で |
| Modal | `MODAL_TOKEN_ID`、`MODAL_TOKEN_SECRET` | `modal` CLI と Python クライアント (api.modal.com と api.modal2.com への HTTP/2 の gRPC)。アプリの実行やデプロイ (費用がかかる) もできます。**注意:** これを付けたサンドボックスは Modal でコードを実行できます。Modal からはインターネットに自由に出られるので、データの行き先はサンドボックスのネットワークポリシーで制限できなくなり、そのコードはワークスペースの Modal Secrets も読めます |
| Cloudflare | `CLOUDFLARE_API_TOKEN` | 読み取りだけ (api.cloudflare.com/client/v4)。デプロイや DNS の変更は提案の承認で。`CLOUDFLARE_ACCOUNT_ID` は秘密ではないので自分で設定 |

実機で確かめたのは今のところ GitHub と「Atlassian (MCP)」だけです。プロファイルは、mise とエージェント自身の更新機能が置く場所のエージェントの実行ファイルを指しています (例: Claude Code は `/home/dev/.local/share/claude/versions/*` と、mise の `/home/dev/.local/share/mise/installs/claude/*/claude`)。

種類ごとの OpenShell のプロファイルは「プロファイルを YAML で見る」で見られます。どこから来たものか (Iwato 同梱、カスタム、ゲートウェイから) を印で示します。OpenShell の CLI で取り込んだプロファイルはゲートウェイから読み、OpenShell の形式の YAML に書き出すので、元のファイルのコメントや書式は残りません。

Iwato にないプロファイルのプロバイダー (OpenShell の CLI で作ったものなど) は「その他」にプロファイルの id とともに並びます。ほかと同じく外したり削除したりできますが、値は Iwato では変えられません。

## 認証情報の送り先

認証情報は、ポリシーの宛先のうち認証情報を付けたものにだけ送られます ([ADR 0022](../../adr/0022-iwato-owns-the-sandbox-policy.md))。Iwato の種類は自分の宛先を持たず、プロバイダーを付けるとその規則がサンドボックスのポリシーに加わり (「認証情報: 名前」と表示)、外すとその認証情報を付けた宛先がすべてポリシーから外れます。規則が許すのは種類に要るものです: GitHub は読み取り (API の GET、GraphQL の query) と clone・fetch、Claude Code・Grok Build・OpenRouter はそれぞれの API、「Atlassian (MCP)」は読み取るだけのツール、ほかは表のとおりです。規則はほかの規則と同じように直せます。

書き込みは提案から許可します。認証情報を付けた宛先の下への提案を承認すると、最初から認証情報が付きます。認証情報の宛先のホストへの提案では、承認の画面で付けるかを選べます (最初は付けません)。プロバイダーはどのサンドボックスでも共通ですが、規則はサンドボックスごとです。

プロキシで絞れないものは、認証情報自体で絞ってください:

- GitHub: GraphQL の引数と、push で書き込むブランチは見られません。使うリポジトリと権限に絞った [fine-grained token](https://docs.github.com/en/authentication/keeping-your-account-secure/managing-your-personal-access-tokens) を使います。
- 「Atlassian (MCP)」: ツールの引数 (どのサイト・プロジェクトか) は見られないので、OAuth のスコープで絞ります。MCP はプロバイダーの画面に出るコマンドで登録してください。URL の最後が `?tools=all` なので、どのツールも自分の名前で呼ばれます。付けないと Atlassian はほとんどのツールを `execute` 系のツール経由で呼び、そのツールは引数でツールの名前を渡すので読み取りのツールになりません。付けずに登録したものは `claude mcp remove atlassian` で消して登録し直せます。

これより前に作ったプロバイダー (それぞれ自分のプロファイルを持つもの) は、作ったときのまま動き、そう表示されます。規則をポリシーで扱うには追加し直してください。

## カスタムのプロバイダー

「カスタムを YAML で追加」で、OpenShell のプロバイダーのプロファイルとして自分の種類を追加できます。Iwato は保存の前に lint と自分の検査をし、設定フォルダーの `profiles/ID.yaml` に保存します (Iwato の起動時に読むので、ほかのエディタでも直せます)。`iwato-` で始まる ID は Iwato のもののために予約されています。

## OAuth のプロバイダー

Atlassian、Claude のアカウントでのログイン、xAI のサブスクリプションでは、Iwato がログインを行い、refresh token をゲートウェイに渡し (以後はゲートウェイが更新します)、自分では持ちません ([ADR 0009](../../adr/0009-refresh-tokens-stay-in-the-gateway.md))。Iwato の設定フォルダーに refresh token が置かれることはありません。

### Atlassian (Jira、Confluence)

「Atlassian (MCP)」のプロバイダーを追加すると、スコープ (既定は Jira と Confluence の読み書き・検索、ユーザー情報、更新用の `offline_access`) を選んで Atlassian の認可ページを開きます。スマホなど別の端末で許可したときは、許可のあとに開けなかったページのアドレスをコピーして Iwato に貼り付けます。サンドボックスでは、「プロバイダー」に表示されるコマンドで `claude` に MCP サーバーを登録します。「プロバイダー」には更新の状態が出て、「今すぐ更新」ですぐに更新できます。許可を取り直す必要があるときは「再接続」を使います (Claude のログインと xAI のサブスクリプションも同じ)。再接続するとプレースホルダーが変わるので、付けているサンドボックスは再起動が要ります。

### Claude Code の Claude のアカウントでのログイン

Iwato が `claude auth login` と同じく、Claude Code 自身の公開クライアント ID でログインします (Anthropic が Iwato に発行したものではありません)。PC では「Claude でログイン」で許可すると自動で Iwato に戻ります。スマホでは「スマホなどでログイン (コードを貼る)」で許可し、Claude のページに出たコード (`コード#…`) を貼り付けます。サンドボックスの `claude` は `CLAUDE_CODE_OAUTH_TOKEN` を読みます。`/usage` が使えるよう、Iwato がトークンのスコープ (`CLAUDE_CODE_OAUTH_SCOPES`) も設定します (`~/.config/iwato/env`)。動いているサンドボックスに付けたときは、新しいターミナルを開いてください。claude.ai のコネクターはプロバイダーがそのホストを許可していないので使えません。確かめるには `claude -p 'hello'`。

サブスクリプション (Free、Pro、Max) の OAuth トークンの扱いは Anthropic の規約が定めています: [Legal and compliance](https://code.claude.com/docs/en/legal-and-compliance)。トークンを使うのはサンドボックスの Claude Code だけですが、ログインは Iwato が Claude Code のクライアント ID で行います。ゲートウェイの最初の更新が失敗すると Iwato がそう表示します。

「Claude Code (セットアップトークン)」の種類はなくなりました。この種類で作ったプロバイダーはそのまま使え、「その他」に表示されます。外すことも削除することもできます。

### Grok Build の xAI のサブスクリプション

Iwato が auth.x.ai にデバイスコードでログインします (PC でもスマホでも同じ手順)。ログインには Grok Build 自身の公開クライアント ID を使っており、xAI が Iwato に発行したものではありません。サンドボックスでは、`grok` がこのトークンを使うよう、Iwato がターミナルに `GROK_AUTH_PROVIDER_COMMAND` を設定します (`~/.config/iwato/env` に書き、標準イメージのシェルが読みます)。前に `grok login` していたら `grok logout` してください。確かめるには `grok -p 'hello'`。
