# 使い始める

[English](../getting-started.md)

## 前提

- Windows 11 と WSL 3.0.1 以上 (WSLc の `wslc.exe` が入ります)。`wslc.exe --version` で確認します
- x64 か Arm64 の PC。ほかには何も要りません (Docker Desktop もアカウントも不要)
- または (試験的) Apple Silicon の Mac か Linux と Docker 互換のエンジン: [macOS](#macos-試験的) を参照

## インストール

[Releases](https://github.com/mtsmfm/iwato/releases) から `iwato-windows-amd64.exe` (または `iwato-windows-arm64.exe`) をダウンロードし (最初のリリースまでは、`main` の変更のたびに作り直されるプレリリース [edge](https://github.com/mtsmfm/iwato/releases/tag/edge) から)、専用のフォルダーに `iwato.exe` として置きます。自分でビルドするときは [開発](../../dev/development.md) (英語) を参照してください。

exe は署名されていないので、ダウンロードしたものを初めて実行するときに Windows の SmartScreen が警告することがあります (「詳細情報」→「実行」)。

Iwato を起動したフォルダーが **設定フォルダー** になります。`iwato.json`、ログ、秘密、自分のプリセットとプロバイダーのプロファイルをそこに置きます。毎回同じフォルダーで起動してください (または `-config PATH\iwato.json` を付けます)。

## 最初の起動

```powershell
.\iwato.exe
```

Iwato は次を行います。

1. `iwato.json` がなければ作ります (サンドボックス 0 個)
2. 起動時の確認 (WSLc・設定)。WSLc を使えないときは、画面に理由と直し方を出し、使えるようになると自動で続けます (「再確認」ですぐ確かめます)。確認の一覧は「設定」にあります
3. 標準イメージとゲートウェイのイメージを ghcr.io から取得します (初回は数分。出力は `image-build.log`)
4. OpenShell のゲートウェイを WSLc で起動します

コンソールに `http://127.0.0.1:17600/#token=...` のような URL が出るので、ブラウザで開きます。10 分以内・1 回限りです。共有しないでください。17600 が使用中なら次の空いているポート (17669 まで) を使います。`-listen 127.0.0.1:PORT` で固定もできます。

イメージとゲートウェイの準備の間も画面は開けます (進み具合を表示)。準備が終わると (失敗したときは理由を添えて)、コンソールにログインの URL をもう一度表示します。まだ使われていなければ同じ URL、期限切れなら新しい URL、もう使われていれば何も出しません。

あとは:

1. 「サンドボックスを作成」で、名前とイメージ (空欄なら標準イメージ) を指定します。[サンドボックス](sandboxes.md)
2. 「起動」して、ターミナルを開きます
3. 中にエージェントを入れます。[エージェント](agents.md)

## ログインとほかの端末

- 一度ログインしたブラウザは、セッションの期限 (既定 8 時間。`-session-hours 1`〜`720`) まで、Iwato を再起動してもログインしたままです
- ほかのブラウザや端末でログインするには、サイドバーの「別端末でログイン」を使います。1 回限りのリンクと QR コード (10 分以内) が出るので、その端末でリンクを開くか、「ログイン」に貼り付けます
- スマホから使うには Tailscale が要ります。[スマホから使う](publishing.md)
- プッシュ通知はサイドバーの「通知を有効化」で有効にします。[ターミナル](terminals.md)
- ブラウザのメニューからアプリ (PWA) としてインストールできます

## コンソールとログ

コンソールに出すのは、起動時の表示、ログインの URL、起動時の確認に問題があるときの 1 行の案内 (詳しくは画面)、対応が要る警告とエラー (準備の失敗、ドライバの停止、ポートの問題など) だけです。そのほか (ゲートウェイやドライバの出力、イメージのビルド、リクエスト) は設定フォルダーの `iwato.log` (10 MB で `iwato.log.1` に移す) とサイドバーの「ログ」に出します。`.\iwato.exe serve -verbose` でコンソールにもすべて出します。

## コマンド

```
iwato                 iwato.json がなければ作り、起動する
iwato serve [-config iwato.json] [-listen 127.0.0.1:PORT] [-public-origin https://HOST.TAILNET.ts.net] [-session-hours 8] [-verbose]
iwato version
iwato help
```

Ctrl+C で終了します。Iwato を終了・更新してもサンドボックスは止まりません。標準イメージならターミナルも動き続け、次に起動したときにつなぎ直します ([ターミナル](terminals.md))。

## macOS (試験的)

macOS では、WSLc の代わりに Docker 互換のエンジンでサンドボックスを動かし、コンピュートドライバは OpenShell の Docker ドライバをそのまま使います ([ADR 0002](../../adr/0002-macos-docker-compatible-runtime.md))。Windows は WSLc のままです。Linux でも Docker Engine で同じように動きます。

実機の Mac ではまだ試していません (CI の Linux の Docker で `TestDockerE2E` を、Mac のランナーで macOS 版のテストを流しています)。動かないところがあれば知らせてください。

### 前提

- Apple Silicon の Mac (ビルドは `darwin-arm64`)
- Docker Engine 28.0 以上の Docker 互換のエンジン (OpenShell の前提)。おすすめは Colima (無料、MIT)。Docker Desktop・OrbStack でも動きますが、商用では有料のことがあります
- Iwato がイメージを自分でビルドするとき (開発版はゲートウェイのイメージをビルドします) は `docker buildx`

### Colima を用意する

```sh
brew install colima docker docker-buildx
colima start --cpu 4 --memory 8
```

`colima start` で Docker の context が `colima` になり、Iwato はそれを使います (Iwato は `docker` コマンドを実行するので、`DOCKER_HOST`・`DOCKER_CONTEXT`・`docker context use` でエンジンを選べます)。buildx は `brew` が表示する手順 (`~/.docker/config.json` に `cliPluginsExtraDirs`) に従います。

サンドボックスは Colima の Linux の VM で動きます。VM に割り当てた CPU とメモリは、サンドボックスを止めても戻りません。変えるときは `colima stop` してから `colima start --cpu N --memory GiB`。

Docker Desktop では、ホストネットワークを有効にし (Settings → Resources → Network → Enable host networking。OpenShell の supervisor が使います)、Enhanced Container Isolation は無効のままにします。

### Iwato を動かす

リリースと Homebrew の tap ができるまでは、プレリリース [edge](https://github.com/mtsmfm/iwato/releases/tag/edge) の `iwato-darwin-arm64` を `iwato` として使います。署名していないので、Gatekeeper に止められたら:

```sh
chmod +x iwato
xattr -d com.apple.quarantine iwato
./iwato
```

`./iwato` は最初にエンジンを確かめます: `docker` があってエンジンが動いているか、版、VM のアーキテクチャ・CPU・メモリ、上の Docker Desktop の設定。結果は「設定」の「起動時の確認」にあります。エンジンが動いていないときは画面に起動のしかたを出し、動き出すと続けます。あとは Windows と同じです (上記)。

### Windows との違い

- ホームを残せるのは、サンドボックスの決まり ([ADR 0005](../../adr/0005-sandbox-image-convention.md)。標準イメージなど) に沿ったイメージだけです。OpenShell の Docker ドライバは作業ディレクトリやその親にボリュームを置けないので、`WORKDIR` がホームのイメージは起動できません
- GPU は使えません (Mac の GPU をコンテナに渡す方法がありません)
- 秘密 (公開のシークレット) は、ログインキーチェーンが持つ鍵 (項目 `iwato` / `secret-file-key`) で暗号化して保存します。Linux では暗号化しないファイル (権限 0600) です
- `iwato.json` に `wslc` はありません。`docker` で CLI のパス (既定は `PATH` の `docker`)、`dockerSocket` でエンジンの中の API のソケットの場所 (既定は `/var/run/docker.sock`。Colima・Docker Desktop・OrbStack の VM の中はこれ。Linux の rootless Docker では CLI の `unix://` の接続先) を指定できます

## 次に

- [サンドボックスとイメージ](sandboxes.md)
- [通信のポリシーと承認](network-policy.md)
- [プロバイダー (認証情報)](providers.md)
- [すべての資料](README.md)
