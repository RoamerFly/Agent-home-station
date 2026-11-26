package companion

// Applies to both the compact social prompt and the full direct/task prompt.
// Free-text preferences are interpreted in the existing model call. They are
// not converted into unconditional must_reply or additional permissions.
const conversationPreferencePolicy = `
conversation_preferences优先于一般旁观沉默判断。分享总结依据实际读到的画面、音轨或正文；结合当前取消状态及已回复内容决定是否继续。`
