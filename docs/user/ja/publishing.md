# ポートの公開とスマホからの利用

[English](../publishing.md)

サンドボックスで動かしたサーバー (Vite、Rails、Jupyter、デスクトップなど) は、カードの「ポート」タブのリンクで開きます。ここには、サンドボックスのプロセスが待ち受けている (0.0.0.0・127.0.0.1・::・::1 で) ポートがリンク付きで出ます (5 秒ごとに更新)。ポートごとの承認は要らず、エージェントが自分で公開することもできません。

公開のしかたは「設定」で選びます: **「この PC だけ」** (既定)、**「Tailscale (スマホからも)」**、**「公開しない」**。

## この PC だけ (既定)

各ポートは `http://<ポート>.<サンドボックス>.localhost:<Iwato のポート>/` で開きます (例: `http://5173.dev.localhost:17600/`)。アカウントも設定も要りません。

- この PC のブラウザー (Chrome・Edge・Firefox) で開きます。これらのブラウザーは `*.localhost` を自分でこの PC に解決し、セキュアコンテキストとして扱います (Service Worker やクリップボードが使えます)
- 認証はありません: この PC のブラウザーなら誰でも開けます (手元で開発サーバーを動かすのと同じ)。Iwato の画面は `http://127.0.0.1:…` のままで、これらのアドレスでは出ません
- HTTP と WebSocket だけです。TCP (psql など) は通りません。必要なら Tailscale を使います
- Iwato は `Host` ヘッダーを `localhost:PORT` に書き換えます (Vite のホストの検査や HMR が localhost と同じように動きます)
- ブラウザー以外のプログラムは `*.localhost` を解決できないことがあります (Windows の名前解決、PowerShell、Python など)。curl なら: `curl --resolve 5173.dev.localhost:17600:127.0.0.1 http://5173.dev.localhost:17600/`
- 名前が DNS のラベルとして正しくないサンドボックス (ハイフンで終わる、3・4 文字目が `--` など) にはリンクがありません
- 何も待ち受けていないポートはエラーのページ (502)、停止中のサンドボックスは 503 です

残る危険 (Iwato なしでサーバーを動かすのと同じとして受け入れています): ほかのサイトのページから、`Origin` を見ない開発サーバーやデバッガーにリクエストを送れます (応答は読めません。Chrome は許可を求めます)。同じ PC のほかの OS ユーザーもポートを開けます。Iwato のポートを生の TCP のまま中継しないでください (`tailscale serve --tcp`、`netsh interface portproxy` など): 中継された接続はこの PC からのものに見え、認証なしで公開したポートに届いてしまいます。

## Tailscale (任意)

Tailscale を使うと、サンドボックスごとに tailnet のノードができ、各ポートを `https://iwato-<サンドボックス>.<tailnet>.ts.net:<ポート>/` でスマホやほかの端末から開けます。TCP もそのまま通ります (`psql`、`ncat`)。Tailscale のアカウントと OAuth クライアントが要ります。

準備 (一度だけ):

1. Tailscale の管理画面の DNS で、MagicDNS と HTTPS Certificates を有効にする
2. tailnet のポリシー (Access controls) に tag を足す (「設定」にコピーボタンがあります):

   ```json
   "tagOwners": {
     "tag:iwato": ["autogroup:admin"]
   }
   ```

   既定の「全員 → 全部」の grant を消している場合だけ、`{"src": ["autogroup:member"], "dst": ["tag:iwato"], "ip": ["*"]}` も足す
3. Settings → OAuth clients → Generate OAuth client: スコープは Keys → Auth Keys の Write だけ、tag は `tag:iwato`
4. Iwato の「設定」で「Tailscale (スマホからも)」を選び、クライアントのシークレット (`tskey-client-…`) を貼って保存。「通すログイン名」に自分の Tailscale のログイン名 (例: `alice@github`。`tailscale serve` 経由で開いていれば自動で入ります) を入れて保存。状態が「公開中」になります

振る舞い:

- 開けるのは「通すログイン名」に入れた人の、tag の付いていない端末だけです。tag 付きのノード (他のサンドボックスやサーバー) と、他の tailnet から共有された端末は通しません
- 平文の `http://` でも開けます (tailnet の中はすでに暗号化されています)。リンクは `https://` です
- 停止中のサンドボックスのノードは残り、ポートは 503 です。サンドボックスを削除するとノードはログアウトします
- シークレットは `iwato.json` には入れず、同じフォルダーの `ingress-secret.bin` に Windows の DPAPI で暗号化して保存します (macOS ではキーチェーンが持つ鍵で暗号化、Linux では暗号化せず権限 0600)。環境変数 `IWATO_TS_AUTHKEY` があればそちらを使います。ノードの状態は設定フォルダーの `ingress\tailscale\<サンドボックス>\` です
- OAuth クライアントの代わりに、再利用できる auth key (`tskey-auth-…`) も使えます

### ポートごとの設定

画面はまだないので、`iwato.json` のサンドボックスごとに書きます:

```json
"sandboxes": [{"name": "dev", "ingress": {"ports": {"5432": {"mode": "tcp"}, "3001": {"host": "preserve"}, "9229": {"disabled": true}}}}]
```

- `"disabled": true` でそのポートを閉じます (404)。サンドボックスの `ingress` に `"disabled": true` を書くと、サンドボックス全体を閉じます
- `"mode": "tcp"` で生の TCP を通します (Tailscale だけ。この PC だけの公開では、開けないことを表示します)
- `"host"`: `localhost` (既定。`Host: localhost:PORT` にし、公開の `Origin` を `http://localhost:PORT` に書き換える)、`forwarded` (`Host: localhost:PORT` に加えて `X-Forwarded-Host`。Rails の `config.hosts` 向け)、`preserve` (公開のホストのまま)

## スマホから Iwato を使う

Iwato の画面は `127.0.0.1` だけで待ち受けます。スマホから使うには、Windows で `tailscale serve` を前に置き、Iwato に HTTPS のアドレスを教えます:

```powershell
tailscale serve --bg http://127.0.0.1:17600
.\iwato.exe serve -public-origin https://ホスト名.tailnet名.ts.net
```

そのうえで「別端末でログイン」を押し、(tailnet に参加した) スマホで QR コードを読み取ります。プッシュ通知と、アプリパスワードでの WebDAV のドライブにも、このアドレスが要ります。ポートの公開のしかたとは別なので、ポートは「この PC だけ」に公開しつつ、スマホから Iwato を使うこともできます。

公開の仕組みの詳細 (英語): [docs/dev/ingress.md](../../dev/ingress.md)。
