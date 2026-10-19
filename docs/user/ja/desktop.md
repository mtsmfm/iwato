# デスクトップと GPU

[English](../desktop.md)

標準イメージには、GUI のアプリ用のデスクトップが入っています: Xfce・Firefox・Selkies (ブラウザで見るリモートデスクトップ) と Mesa の d3d12 です。特定のエージェントに依らず、すべてサンドボックスのユーザー `dev` (root なし) で動きます。根拠と未確認の点 (英語): [docs/dev/gpu.md](../../dev/gpu.md)。

## 起動と表示

- 起動: サンドボックスのターミナルで `iwato-desktop` (裏で起動します。ログは `~/.cache/iwato-desktop.log`)。すでに動いていればそう表示します。`iwato-desktop status` で状態、`iwato-desktop stop` で停止、`iwato-desktop run` は前面で動かします
- 表示: 「ポート」タブで **6080** のリンクを開きます ([ポートの公開](publishing.md))。デスクトップ自体にログインはありません: loopback だけで待ち受け、誰が開けるかは公開のしかたで決まります (「この PC だけ」ならこの PC のブラウザー、Tailscale なら「通すログイン名」に入れた人の端末)
- サンドボックスの起動時に自動では起動しません。サンドボックスを停止するとデスクトップは突然終わりますが、残った古いロックやソケットは次の `iwato-desktop` が片付けます
- デスクトップの中では、リンクはそのデスクトップの Firefox で開きます (`BROWSER=firefox`)。`iwato-desktop-doctor` で、リンクをクリックしたときに何が使われるかを表示します
- `IWATO_DESKTOP_PORT` でポートを、`IWATO_DESKTOP_ARGS` で Selkies のオプションを足せます (例: `--encoder=jpeg`)

## GPU

GPU は任意です。サンドボックスの作成時に「GPU を使う」を入れると (作成のときだけ指定できます)、GPU で描画・エンコードします。入れなくても CPU 描画で動きます。WSLc は GPU をまるごと渡すので (`--gpus all`)、台数や機種は選べません。カードに GPU の印が出ます。

- NVIDIA は WSL と同じく CUDA が使えます。AMD と Intel は D3D12 / DirectML と Mesa の d3d12 です (`/dev/dri` はありません)
- どのサンドボックスにも 1 GB の `/dev/shm` を付けます (Firefox は Docker の既定の 64 MB では落ちるため)
- Docker のエンジンでは、Docker が CDI で NVIDIA の GPU を使えるとき (NVIDIA Container Toolkit の CDI の定義) だけ「GPU を使う」が出ます。macOS では出ません ([ADR 0002](../../adr/0002-macos-docker-compatible-runtime.md))
