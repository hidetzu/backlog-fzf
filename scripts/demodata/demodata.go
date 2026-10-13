// Package demodata holds the fictional Backlog space shared by the README
// demo (scripts/demo-seed) and the semantic search evaluation
// (scripts/eval). Nothing here comes from a real customer.
package demodata

import (
	"strings"
	"time"

	"github.com/hidetzu/backlog-fzf/internal/backlog"
)

// Base is the reference "now" for all seeded timestamps. Fixed so the
// demo recording and the evaluation are identical on every run.
var Base = time.Date(2026, 9, 30, 9, 0, 0, 0, time.UTC)

func daysAgo(n int) time.Time { return Base.AddDate(0, 0, -n) }

func due(n int) *time.Time {
	t := Base.AddDate(0, 0, n)
	return &t
}

type issueSeed struct {
	key, summary, status, assignee string
	description                    string
	updated                        int // days ago
	due                            *time.Time
}

var issues = []issueSeed{
	{"WEB-142", "ログイン画面で認証エラーが表示される", "処理中", "佐藤",
		"Safari で SSO ログイン後に「認証に失敗しました」と表示される。\n\n## 再現手順\n1. Safari でトップを開く\n2. SSO でログイン\n3. リダイレクト後にエラー\n\n## 調査メモ\nCookie の SameSite 属性が原因の可能性が高い。", 0, due(3)},
	{"WEB-139", "認証トークンの有効期限を延長する", "未対応", "鈴木",
		"現在 1 時間で切れるため、リフレッシュトークンで 30 日まで延長したい。", 2, due(14)},
	{"WEB-131", "パスワードリセットメールが届かない", "完了", "佐藤",
		"SPF レコードの設定漏れが原因。DNS を修正して解決。", 9, nil},
	{"WEB-128", "Add dark mode to dashboard", "処理中", "Alice",
		"Follow the design tokens in the style guide. Respect prefers-color-scheme.", 4, due(10)},
	{"WEB-120", "検索結果のページングが遅い", "未対応", "田中",
		"10,000 件を超えると OFFSET が重い。カーソルページングに変更する。", 12, nil},
	{"WEB-115", "Upgrade React to v19", "完了", "Bob",
		"All tests passing after migrating to the new JSX transform.", 20, nil},
	{"API-88", "決済 API のタイムアウトを 30 秒に変更", "処理済み", "高橋",
		"外部決済代行のレスポンスが遅延するケースがあるため。", 1, nil},
	{"API-85", "OAuth 認証フローに PKCE を導入", "処理中", "鈴木",
		"モバイルアプリ向けに PKCE (RFC 7636) を必須化する。", 3, due(7)},
	{"API-80", "Rate limit headers are missing on 429", "未対応", "Alice",
		"Return X-RateLimit-Remaining and Retry-After on every 429 response.", 6, nil},
	{"API-74", "ユーザー一覧 API に検索条件を追加", "完了", "田中",
		"部署・役職での絞り込みに対応。", 15, nil},
	{"API-71", "Deprecate v1 endpoints", "未対応", "Bob",
		"Announce sunset date; add Deprecation header.", 25, due(60)},
	{"OPS-57", "本番環境のデプロイ手順を自動化", "処理中", "伊藤",
		"GitHub Actions から ECS へのデプロイを自動化する。手動手順はドキュメント参照。", 1, due(5)},
	{"OPS-55", "Postgres 16 へのアップグレード", "未対応", "伊藤",
		"ステージングで検証後、メンテナンスウィンドウで実施。", 5, due(21)},
	{"OPS-52", "監視アラートの閾値を見直す", "完了", "高橋",
		"CPU 80% 超が 5 分継続でアラートに変更。", 11, nil},
	{"OPS-49", "Rotate TLS certificates before expiry", "処理済み", "Bob",
		"Certificates expire on 2026-10-15. Automate with cert-manager.", 8, due(15)},
	{"OPS-46", "ログ保存期間を 90 日に統一", "完了", "伊藤",
		"", 30, nil},
	{"APP-33", "iOS アプリでプッシュ通知が二重に届く", "処理中", "山本",
		"FCM と APNs の両方に登録されていた。", 2, due(4)},
	{"APP-30", "生体認証でのログインに対応", "未対応", "山本",
		"Face ID / Touch ID / Android BiometricPrompt に対応する。", 7, due(30)},
	{"APP-27", "Crash on launch on Android 15", "完了", "Alice",
		"Fixed by updating the WebView dependency.", 18, nil},
}

type docSeed struct {
	id, project, title, body string
	updated                  int // days ago
}

var docs = []docSeed{
	{"0192a0f0c1d2e3f4a5b6c7d8e9f00001", "WEB", "認証基盤 設計書",
		"# 認証基盤 設計書\n\n## 概要\nSSO / OAuth 2.0 / パスワード認証を統一的に扱う。\n\n## トークン\n- アクセストークン: 1 時間\n- リフレッシュトークン: 30 日", 1},
	{"0192a0f0c1d2e3f4a5b6c7d8e9f00002", "OPS", "本番デプロイ手順書",
		"# 本番デプロイ手順書\n\n1. main ブランチにマージ\n2. タグを打つ\n3. GitHub Actions の deploy ジョブを承認", 3},
	{"0192a0f0c1d2e3f4a5b6c7d8e9f00003", "API", "API Design Guidelines",
		"# API Design Guidelines\n\n- Use plural nouns for resources\n- Return RFC 7807 problem details on errors\n- Paginate with cursors", 10},
	{"0192a0f0c1d2e3f4a5b6c7d8e9f00004", "WEB", "リリースノート 2026年9月",
		"# リリースノート 2026年9月\n\n- ダークモード（β）\n- 検索の高速化\n- 認証まわりの不具合修正", 5},
	{"0192a0f0c1d2e3f4a5b6c7d8e9f00005", "OPS", "障害対応フロー",
		"# 障害対応フロー\n\n1. #incident チャンネルで宣言\n2. 一次切り分け\n3. ポストモーテムを Backlog に記録", 14},
	{"0192a0f0c1d2e3f4a5b6c7d8e9f00006", "APP", "Onboarding guide for mobile team",
		"# Onboarding\n\nSet up Xcode, Android Studio, and the shared signing keys.", 22},
}

// Projects lists the project keys used by the fictional space.
var Projects = []string{"WEB", "API", "OPS", "APP"}

// Issues returns the fictional issues with ids 1..n in seed order.
func Issues() []backlog.Issue {
	out := make([]backlog.Issue, len(issues))
	for n, s := range issues {
		updated := daysAgo(s.updated)
		out[n] = backlog.Issue{
			ID:          n + 1,
			Key:         s.key,
			ProjectKey:  projectKey(s.key),
			Summary:     s.summary,
			Description: s.description,
			Status:      s.status,
			Assignee:    s.assignee,
			DueDate:     s.due,
			CreatedAt:   updated.AddDate(0, 0, -7),
			UpdatedAt:   updated,
		}
	}
	return out
}

// Documents returns the fictional documents in seed order.
func Documents() []backlog.Document {
	out := make([]backlog.Document, len(docs))
	for n, s := range docs {
		updated := daysAgo(s.updated)
		out[n] = backlog.Document{
			ID:         s.id,
			ProjectKey: s.project,
			Title:      s.title,
			Body:       s.body,
			CreatedAt:  updated.AddDate(0, -1, 0),
			UpdatedAt:  updated,
		}
	}
	return out
}

// projectKey returns the "WEB" part of "WEB-142".
func projectKey(issueKey string) string {
	key, _, _ := strings.Cut(issueKey, "-")
	return key
}
