# サンドボックスのコーディングエージェント

[English](../agents.md)

標準イメージにはエージェントの CLI も設定も入っていません。サンドボックスの中で、サンドボックスのユーザーとして、mise で入れます。すべてホームに入るので、サンドボックスを作り直しても残ります ([サンドボックス](sandboxes.md))。

## インストールの通信

プリセット「ツールのインストール」と「エージェントの CLI」(どちらも作成フォームの既定) で、エージェントのインストールと更新の通信が最初から許されます ([プリセット](network-policy.md#プリセット))。あとから「ポリシー」タブで入れることもできます。

エージェントの API の宛先 (`api.anthropic.com`、`api.x.ai`、`chatgpt.com` など) はプリセットに含まず、エージェントのプロバイダーを付けるか、エージェントの提案を承認します ([プロバイダー](providers.md))。プロバイダーを付けておけば、`mise use -g claude` のあとすぐ `claude` が使えます。ほかに要る通信は、エージェントが提案します ([提案](network-policy.md#規則の提案))。

## Claude Code

```sh
mise use -g claude
```

`~/.local/share/mise/installs/claude/<版>` に入ります。Claude Code 自身の更新は `~/.local/share/claude/versions/<版>` に入ります。API (`api.anthropic.com:443`) は「Claude Code」のプロバイダー ([プロバイダー](providers.md)) を付けるか、提案を承認します。

## Grok Build

```sh
mise use -g grok
```

`~/.local/share/mise/installs/grok/<版>` に入ります。grok 自身の更新は `~/.grok/downloads` に入ります。プロバイダーは「Grok Build」(SuperGrok など個人の xAI のアカウントでログイン) を使います ([プロバイダー](providers.md))。

## Codex

```sh
mise use -g codex
```

mise は GitHub の openai/codex のリリースから入れます。codex は新しい版を知らせるだけで、自分では更新しません (`mise use -g codex@latest` で更新します)。プロバイダーは「Codex」(ChatGPT のアカウント) か、API キーなら「OpenAI API」を使います ([プロバイダー](providers.md#codex-の-chatgpt-でのログイン))。

## そのほかのエージェント

提案を承認して使います。

## エージェント向けのサンドボックスの説明 (`/AGENTS.md`)

標準イメージには `/AGENTS.md` があり、エージェントに環境を説明します: 止められた通信の見え方と規則の提案のしかた、残るもの、認証情報が置き換え用の値であること、パッケージマネージャーの既定 ([サンドボックス](sandboxes.md#パッケージマネージャーの既定))、HTTPS の git、デスクトップ。Claude Code も Codex も `/AGENTS.md` を自分では読まないので、Iwato はサンドボックスを起動するたびに次を **ないときだけ** 置きます:

- Claude Code: `~/.claude/CLAUDE.md` (`@/AGENTS.md` の 1 行で取り込む)
- Codex: `~/.codex/AGENTS.md` (`/AGENTS.md` へのシンボリックリンク)

あるファイル (中身が何でも、壊れたシンボリックリンクでも) は変えません。ホームがない・サンドボックスのユーザーのものでない・シンボリックリンクのとき、`~/.claude` や `~/.codex` がシンボリックリンクやファイルのとき、イメージに `/AGENTS.md` がないときは何もしません。置いてほしくなければ、自分の `~/.claude/CLAUDE.md` や `~/.codex/AGENTS.md` を作ります (空でも構いません。ホームは残ります)。自分のファイルから取り込むには `@/AGENTS.md` の行を足します。その他のエージェントは、そのエージェントの全体の指示ファイルから `/AGENTS.md` を読むように書きます。
