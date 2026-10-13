package main

// evalCase is one paraphrase query and the record(s) a user would be
// looking for. Queries are written so that they share no keyword with
// the target's summary / title: the keyword path should score ~0 on
// them, and the semantic path is judged by Recall@k / MRR.
type evalCase struct {
	query string
	// want lists acceptable targets as "issue:KEY" or "doc:ID". Any of
	// them counts as a hit (some queries legitimately match several).
	want []string
}

func issue(key string) string { return "issue:" + key }
func doc(id string) string    { return "doc:" + id }

// Document ids from scripts/demodata.
const (
	docAuthDesign   = "0192a0f0c1d2e3f4a5b6c7d8e9f00001"
	docDeployGuide  = "0192a0f0c1d2e3f4a5b6c7d8e9f00002"
	docAPIGuide     = "0192a0f0c1d2e3f4a5b6c7d8e9f00003"
	docIncidentFlow = "0192a0f0c1d2e3f4a5b6c7d8e9f00005"
	docOnboarding   = "0192a0f0c1d2e3f4a5b6c7d8e9f00006"
)

var cases = []evalCase{
	// Symptom paraphrases (the classic "I remember what happened, not the words").
	{"Safari でシングルサインオンに失敗する", []string{issue("WEB-142")}},
	{"セッションがすぐ切れるので長くしたい", []string{issue("WEB-139")}},
	{"パスワード再設定の案内メールが来ない", []string{issue("WEB-131")}},
	{"画面を暗い配色に切り替えたい", []string{issue("WEB-128")}},
	{"件数が多いと一覧の表示が重い", []string{issue("WEB-120")}},
	{"スマホの通知が同じ内容で 2 回来る", []string{issue("APP-33")}},
	{"最新の Android で起動直後に落ちる", []string{issue("APP-27")}},
	// Intent paraphrases.
	{"フロントエンドのフレームワークを最新版に上げる", []string{issue("WEB-115")}},
	{"外部の決済サービスの応答待ち時間を伸ばす", []string{issue("API-88")}},
	{"モバイル向けの認可コードフローを安全にする", []string{issue("API-85")}},
	{"リクエスト制限を超えたときのレスポンスヘッダ", []string{issue("API-80")}},
	{"古いバージョンの API の廃止予告", []string{issue("API-71")}},
	{"データベースのメジャーバージョンアップ", []string{issue("OPS-55")}},
	{"証明書の期限切れ前の更新を自動化する", []string{issue("OPS-49")}},
	{"指紋や顔でサインインできるようにする", []string{issue("APP-30")}},
	// Mixed issue / document targets.
	{"本番リリースを手作業からパイプラインに", []string{issue("OPS-57"), doc(docDeployGuide)}},
	{"リリース作業の手順が書いてある資料", []string{doc(docDeployGuide), issue("OPS-57")}},
	{"認証の仕組みをまとめた設計資料", []string{doc(docAuthDesign)}},
	{"障害が起きたときの対応の流れ", []string{doc(docIncidentFlow)}},
	{"新しく入ったモバイル開発者向けの準備手順", []string{doc(docOnboarding)}},
	{"REST のエンドポイント命名のルール", []string{doc(docAPIGuide)}},
	// English query against Japanese records (cross-lingual).
	{"users cannot sign in with single sign-on", []string{issue("WEB-142")}},
}

// partialCases are queries where the user remembers one exact term (a
// product name, an acronym, an error code) plus a vague description.
// The keyword path can find these; the question is whether fusing it
// with the semantic ranking (hybrid) beats semantic alone.
var partialCases = []evalCase{
	{"Safari の認証が通らない", []string{issue("WEB-142")}},
	{"SameSite が原因っぽい件", []string{issue("WEB-142")}},
	{"PKCE を入れたい", []string{issue("API-85")}},
	{"Postgres を上げる作業", []string{issue("OPS-55")}},
	{"React のメジャーアップデート", []string{issue("WEB-115")}},
	{"cert-manager で更新を自動化", []string{issue("OPS-49")}},
	{"FCM の通知が重複", []string{issue("APP-33")}},
	{"OFFSET が遅い問題", []string{issue("WEB-120")}},
	{"SPF の設定漏れ", []string{issue("WEB-131")}},
	{"429 のときのヘッダ", []string{issue("API-80")}},
	{"ECS へのデプロイ自動化", []string{issue("OPS-57"), doc(docDeployGuide)}},
	{"RFC 7807 のエラー形式", []string{doc(docAPIGuide)}},
}
