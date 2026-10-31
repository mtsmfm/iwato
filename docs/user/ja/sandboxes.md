# サンドボックスとイメージ

[English](../sandboxes.md)

Iwato のサンドボックス 1 つは [NVIDIA OpenShell](https://github.com/NVIDIA/OpenShell) のサンドボックス 1 つ (名前も同じ) で、WSLc のコンテナとして動きます。Iwato が OpenShell のゲートウェイを WSLc で動かし、WSLc 用のコンピュートドライバも Iwato 自身が持ちます。挙動は OpenShell の Docker ドライバに揃えています。

## 作成・起動・停止

サイドバーの「サンドボックスを作成」で作ります:

- **名前**: 英小文字で始まり、英小文字・数字・ハイフンの 32 文字以内。公開するポートの名前 (`5173.名前.localhost`) にも使うので、DNS のラベルにならない名前ではこの PC のリンクが出ません ([ポートの公開](publishing.md))
- **イメージ**: 空欄なら Iwato の標準イメージ (下記)。`openshell sandbox create --from` と同じく任意のイメージも指定できます (例: `ubuntu:24.04`)
- **「GPU を使う」**: この PC の GPU をサンドボックスに渡します (WSL の GPU。台数や機種は選べません)。あとから「作り直す」で変えられ、GPU を使える環境でだけ出ます。[デスクトップと GPU](desktop.md)
- **「通信のプリセット」**: 最初の通信の規則。「パッケージ」「ツールのインストール」「エージェントの CLI」は最初から選ばれ、「よく読む参照先」は選ばれていません。「詳細」に何を許すかが出ます ([通信のポリシー](network-policy.md#プリセット))
- **プロバイダー**: 付ける認証情報 ([プロバイダー](providers.md))

作ると起動してターミナルに接続します。サンドボックスのカードには「起動」と、メニュー (「⋯」) の「停止」「削除」「作り直す」「プロバイダー」があります。

カードのタブは「ターミナル」「ポリシー」「エディタ」「ポート」で、その並びの最後に「⋯」のメニューがあります。メニューの上の方からほかの表示 (「リンク」「テキスト」「ファイル」「リソース」) を開けます。開いている表示はタブとして出ます。エージェントの提案が承認を待っている間は、表示の上に 1 行で出ます。「確認」で承認の画面 (複数あるときは「ポリシー」のタブ) を開き、「あとで」で新しい提案が来るまで隠します。[ターミナル](terminals.md)、[通信のポリシー](network-policy.md)、[ファイルとエディタ](files-and-editor.md)、[ポートの公開](publishing.md) を参照してください。

## リソース

カードの見出しに、CPU (上限に対する割合。上限がなければ PC のコア数に対して)、メモリと上限、GPU を付けていれば GPU のメモリを出します。停止中は最後に測ったホームの大きさです。設定の割合で黄・赤になります。押すと「リソース」を開きます: 直近 10 分の CPU とメモリ (ページを開いている間だけ)、メモリの使用中・キャッシュ・空き、ホームの大きいフォルダ (動作中に 10 分ごと、または「測り直す」で測ります)、GPU のメモリ。WSL では GPU の値は GPU 全体のもので、ほかのサンドボックスや Windows のアプリの分も含みます。サイドバーには動作中のものの合計と PC 全体の量を出し、「ボリューム」には各ホームの大きさを出します。

## ターミナルが終わると停止

サンドボックスは、最後のターミナルが終了するとすぐに停止します。停止するとターミナルの外のプロセス (バックグラウンドの開発サーバー、デスクトップなど) も止まるので、それらを使う間はターミナルを開いたままにしてください。ホームのデータは残り、「起動」で再開できます。

## 残るのはホームの下だけ

- ホーム `/home/dev` (`$HOME`) はいつもボリュームに置かれ、サンドボックスを削除・作り直しても残ります
- ホームの外 (作業ディレクトリ `/sandbox`、`/usr/local`、`/var` など) は停止・起動では残りますが、削除や作り直しで消えます
- 作業は `~/work` で (ターミナルはそこで始まります)
- エージェント、mise で入れたツール、それらの設定とログインはホームにあるので、これも残ります。既定でホームの外にデータを置くツール (`/var/lib` のデータベースなど) は、ホームの下を指すように設定します
- 削除したサンドボックスと同じ名前で作ると、そのホームを引き継ぎます
- サイドバーの「ボリューム」でサンドボックスごとのボリュームを確認でき、使われていないものは削除できます

## 削除

「削除」でサンドボックス名を入力すると、サンドボックスを削除し、プロセスを止めます。「ホームのデータ (ボリューム) も削除する」に印を付けない限り、ホームのボリュームは残ります (付けると元に戻せません)。

## 作り直す

サンドボックスは作ったときのイメージのままです。新しいイメージ (新しい標準イメージなど) にするには「作り直す」を使います。削除して、同じ名前・同じ設定で今のイメージから作り直し、起動します。数分かかり、動いているプロセスとターミナルは終わります。

- 引き継ぐもの: ホームのボリューム、付けているプロバイダー、ポリシーの規則 (承認した提案の規則と手で足した規則。プロバイダーの規則はプロバイダーを付け直すと戻ります)、作成時の設定 (イメージ・通信のプリセット・GPU・公開の設定)
- なくなるもの: ホームの外のファイル、承認待ちと却下した提案、ポリシーの履歴 (OpenShell がサンドボックスごとに持つもの)
- OpenShell が受け付けなくなった規則は戻さず、どれかを知らせます
- 「GPU を使う」で、作り直すサンドボックスの GPU を付けたり外したりできます (GPU を使える環境か、今 GPU を使っているときに出ます)
- 「最新のイメージを取得してから作り直す」に印を付けると、作り直す前に新しい標準イメージを取得します (開発版。下記)
- Windows ではサンドボックスは作ったときの OpenShell のスーパーバイザー (通信のチェック) も使い続けます。Iwato が新しいものを持ってくると、カードに「作り直しで更新」(サイドバーに ↻) が出ます。押すと「作り直す」が開きます

## 標準イメージ

Iwato の標準イメージ (`ghcr.io/mtsmfm/iwato-sandbox`、`sandboximage/` からビルド) は、特定のエージェントに依りません。サンドボックスのユーザー `dev` は root でなく sudo もないので、root なしでは入れにくいものを入れています ([ADR 0004](../../adr/0004-standard-image-contents.md)):

- Ubuntu 24.04 と一般的なツール: git、gh (GitHub のプロバイダー用の git の credential helper つき)、tmux、curl、ripgrep、fzf、zoxide、eza、bat、jq、python3 (OS のもの) など
- mise (ツールは何も指定していません) と starship、Omarchy の bash の既定をもとにしたシェルの設定
- ビルドのツールチェーン (build-essential、pkg-config、cmake、autoconf、automake、libtool、bison) と、mise が Ruby・Python をビルドするのと、よく使う native の gem・wheel に要る開発用ヘッダー (OpenSSL、zlib、libyaml、libffi、readline、GMP、GDBM、SQLite、bzip2、liblzma、ncurses、uuid、libpq、MySQL のクライアント、libxml2、libxslt、libcurl)
- クライアントと CLI: sqlite3、postgresql-client、MySQL のクライアント、unzip、zip、xz-utils、zstd、rsync、openssh-client、imagemagick、ffmpeg、procps、file、patch、tzdata、locales (en_US.UTF-8 と ja_JP.UTF-8)
- Playwright の Chromium と Firefox の動作に要るシステムのライブラリ (ブラウザ自体は `npx playwright install chromium` などでホームに入れます)
- デスクトップ (Xfce、Firefox、Selkies) と Mesa の d3d12 ([デスクトップと GPU](desktop.md))
- `/AGENTS.md`: エージェント向けの環境の説明 (通信と提案、残るもの、認証情報が置き換え用の値であること、git)。[エージェント](agents.md)
- Iwato のサンドボックスの決まり (下記)

言語のランタイム (Node、Python、Go、Ruby、Java など) とエージェントの CLI は入っていません。サンドボックスの中で mise で入れます ([エージェント](agents.md))。

### パッケージマネージャーの既定

悪意のあるパッケージのリリースへの備えとして、mise で入れるパッケージマネージャーの既定をイメージが設定しています ([ADR 0022](../../adr/0022-iwato-owns-the-sandbox-policy.md))。プロジェクトやご自身の設定より優先度が低いので、どちらでも変えられます:

| ツール | 既定 | 場所 | 変え方 |
|---|---|---|---|
| npm (11.10 以降) | `min-release-age=7` (日)、`ignore-scripts=true` | `/etc/iwato/npmrc`。`NPM_CONFIG_GLOBALCONFIG` で npm のグローバル設定として読まれます | プロジェクトの `.npmrc` か `~/.npmrc`。1 回だけなら `--min-release-age=0`、`--ignore-scripts=false` |
| uv (0.9.17 以降) | `exclude-newer = "7 days"` | `/etc/uv/uv.toml` | プロジェクトの `[tool.uv]` か `uv.toml`、`~/.config/uv/uv.toml`。1 回だけなら `--exclude-newer false` (uv 0.11.24 以降) |
| pip | なし (pip 26.0 は期間を受け付けず、今の Python に入っているため) | | pip 26.1 以降で `~/.config/pip/pip.conf` か仮想環境の `pip.conf` に `[global] uploaded-prior-to = P7D`。1 回だけなら `--uploaded-prior-to P7D` |
| Bundler (4.0.13 以降) | `BUNDLE_COOLDOWN=7` (日) | シェルの環境変数 | `bundle config set --local cooldown N`。1 回だけなら `--cooldown 0` |

- これより古い版は設定を無視します。npm 11.10 より前は `min-release-age` を知らないという警告を出すことがあります
- インストールスクリプトの要るパッケージ (native のビルド) は npm では失敗するか、ビルドされないままになります。そのインストールだけ `--ignore-scripts=false` を付けるか、後から `npm rebuild PKG --ignore-scripts=false` を実行します。pnpm 10 以降と Bun は、もともとプロジェクトが許したパッケージにしか依存のスクリプトを実行しません (Bun はよく使われるパッケージの一覧も)。Yarn Berry では `.yarnrc.yml` の `enableScripts: false` で止まります
- uv はクールダウンを `uv.lock` に記録します。クールダウンなしで作られたロックファイルのプロジェクトでは、`uv lock` がロックファイルを書き換え、`uv sync --locked` は失敗します。`uv sync --frozen` はロックファイルのとおりに入れます。クールダウンの要らないプロジェクトは `[tool.uv]` に `exclude-newer = false` を書きます (uv 0.11.24 以降)
- pnpm 11 以降と最近の Yarn Berry には 1 日の既定のクールダウンがあります (`minimumReleaseAge` (分)、`npmMinimalAgeGate`)。延ばすにはプロジェクトかグローバルの設定 (`~/.config/pnpm/config.yaml`、`~/.yarnrc.yml`) に書きます。Bun には既定がなく、グローバルの `~/.bunfig.toml` はホームにあるのでイメージでは設定できません。そこかプロジェクトの `bunfig.toml` に `[install] minimumReleaseAge = 604800` (秒) を書きます
- すべてのシェルで既定をやめるには、`~/.bashrc` に `unset NPM_CONFIG_GLOBALCONFIG BUNDLE_COOLDOWN` を書き、`~/.config/uv/uv.toml` に `exclude-newer = false` を書きます (uv 0.11.24 以降)
- `/AGENTS.md` はエージェントにロックファイルからのインストールを求めています (`npm ci`、`pnpm install --frozen-lockfile`、`uv sync --frozen`、`BUNDLE_FROZEN=true bundle install` など)

### イメージの取得

- リリース版の exe は自分の版のタグのイメージを、プレリリース edge の exe は自分のコミットのイメージを取得します。新しい exe は自分のイメージを持ってきます
- main から自分でビルドした exe は `edge` のタグ (main に取り込まれるたびに公開) を、手元にないときだけ取得します。新しい `edge` は頼んだときだけ取得します: 「設定」の「標準イメージを更新」か、作り直すときの「最新のイメージを取得してから作り直す」。以後に作る・作り直すサンドボックスがそれを使います。古いイメージは使うサンドボックスがなくなってから消し、取得できなければ手元のものを使い続けます
- 手元のイメージの作成・取得の日時は「設定」に出ます

## 自分のイメージとサンドボックスの決まり

どのイメージでも動きます。Iwato のすべての機能を使うには、標準イメージと同じく Iwato のサンドボックスの決まり ([ADR 0005](../../adr/0005-sandbox-image-convention.md)) に従います:

- Iwato のデーモン `/usr/local/lib/iwato/iwato-entrypoint` と、それを示すイメージのラベル `iwato.entrypoint` (Iwato と同じコミットからビルド。ターミナルと、ホームを出す SFTP)
- Policy Advisor の手引き (`/etc/openshell/skills/`) と `/AGENTS.md`
- ホームの外の `WORKDIR` (`/sandbox`) と、ユーザーのホーム `/home/dev`

デーモンのないイメージでは、メインのターミナルがサンドボックスの最初のプロセスになり、追加のターミナルは Iwato の終了で終わります。ファイル・エディタ・画像の貼り付けは OpenShell の SFTP を使い、ホームではなく作業ディレクトリを出します (「ファイル」と「エディタ」に「作業ディレクトリ (…) を開きます。」と出ます)。イメージのデーモンが Iwato より古いときは、使える機能はそのまま使え、残りは同じように切り替わります。作り直すと新しいデーモンになります。
