package companion

// Applies to both the compact social prompt and the full direct/task prompt.
// Free-text preferences are interpreted in the existing model call. They are
// not converted into unconditional must_reply or additional permissions.
const conversationPreferencePolicy = `
本会话管理员设置的 conversation_preferences 优先于普通群聊旁观策略，只调整本会话的表达和参与方式，不改变权限。分享默认总结时无需另行 @；优先读取实际画面、音轨或正文，读取范围不足时明确说明范围，再决定是否补充。继续遵循本会话的表达偏好、当前取消状态和已回复去重。`
