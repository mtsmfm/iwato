# Iwato

[English](README.md)

<img src="web/icon.svg" width="96" alt="">

> [!WARNING]
> Iwato は開発中です。最初の安定版までは、破壊的な変更が入る見込みです (設定・サンドボックス・イメージ・API が、移行手段なしに変わることがあります)。

**Iwato** (岩戸) は、Windows の単一 exe から AI コーディングエージェント (Claude Code、Codex、Grok Build など) の作業場所を [NVIDIA OpenShell](https://github.com/NVIDIA/OpenShell) のサンドボックスとして作り、ブラウザ (スマホ含む) から使うためのツールです。

- **WSLc 上のサンドボックス**: Iwato が OpenShell のゲートウェイを WSLc で動かし、WSLc 用のコンピュートドライバも Iwato 自身が持ちます。ほかに入れるものはありません (Docker Desktop もアカウントも不要)
- **通信は承認制**: サンドボックスの通信は既定ですべて拒否されます。エージェントは OpenShell の Policy Advisor で必要な規則を提案し、あなたが画面で承認・却下します (プッシュ通知あり)
- **認証情報は外に置く**: トークンは OpenShell のプロバイダーに預け、サンドボックスには置き換え用の値だけが渡ります。プロバイダーごとに範囲を決められます (書き込める GitHub のリポジトリなど)
- **動き続けるターミナル**: 画面は Iwato が、プロセスはサンドボックスの中のデーモンが持つので、ブラウザを閉じても Iwato を再起動してもターミナルは続きます
- **ホームは残る**: `/home/dev` はボリュームで、サンドボックスを作り直しても残ります
- **ファイル・エディタ・ポート・デスクトップ**: ホームをネットワークドライブに、エディタのタブ、開発サーバーを `http://<ポート>.<サンドボックス>.localhost:17600/` で、ブラウザで見るデスクトップ

## 前提

- Windows 11 と WSL 3.0.1 以上 (`wslc.exe --version`)
- または (試験的) Apple Silicon の Mac と Docker 互換のエンジン (おすすめは [Colima](https://github.com/abiosoft/colima)。Docker Desktop・OrbStack でも動きます)、または x64 か Arm64 の Linux と Docker Engine

## インストールと最初の起動

[Releases](https://github.com/mtsmfm/iwato/releases) から `iwato-windows-amd64.exe` (または `-arm64`) をダウンロードし (最初のリリースまでは、`main` の変更のたびに作り直されるプレリリース [edge](https://github.com/mtsmfm/iwato/releases/tag/edge) から)、専用のフォルダーに `iwato.exe` として置いて、そこで実行します:

```powershell
.\iwato.exe
```

表示された `http://127.0.0.1:17600/#token=...` を開きます (10 分以内・1 回限り)。初回はイメージの取得とゲートウェイの起動に数分かかります。WSLc やエンジンを使えないときは、画面に理由と直し方が出ます。あとはサンドボックスを作成して起動し、ターミナルを開きます。

macOS (試験的。実機ではまだ試していません) では、[edge](https://github.com/mtsmfm/iwato/releases/tag/edge) から `iwato-darwin-arm64` をダウンロードし、専用のフォルダーに `iwato` として置いて、そこで実行します:

```sh
brew install colima docker docker-buildx
colima start --cpu 4 --memory 8
chmod +x iwato
xattr -d com.apple.quarantine iwato
./iwato
```

Linux (試験的) では、同じように [edge](https://github.com/mtsmfm/iwato/releases/tag/edge) から `iwato-linux-amd64` (または `-arm64`) をダウンロードし、`chmod +x iwato` して、Docker Engine を動かした状態で `./iwato` を実行します。

詳しくは [使い始める](docs/user/ja/getting-started.md) (macOS と Linux は [その節](docs/user/ja/getting-started.md#macos-と-linux-試験的))。

## ドキュメント

- 利用者向け: [docs/user/ja](docs/user/ja/README.md) — サンドボックス、通信のポリシー、プロバイダー、ターミナル、ファイルとエディタ、ポートの公開、デスクトップと GPU、設定、困ったとき
- 開発者向け (英語): [docs/dev/development.md](docs/dev/development.md)、[構成](docs/dev/architecture.md)、[設計の決定 (ADR)](docs/adr/README.md)

## ライセンス

[LICENSE](LICENSE) と [THIRD-PARTY-NOTICES.md](THIRD-PARTY-NOTICES.md) を参照してください。
