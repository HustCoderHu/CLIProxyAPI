# How to Perform OAuth Login

1. **准备配置:** 确保已设置有效的配置对象，包含认证目录和其他必要参数。

2. **选择提供商:** 根据需要选择AI提供商（codex、claude、gemini、qwen或iflow）。

3. **执行登录:** 调用 `sdk/auth/manager.go:47` 的Login方法，传入提供商、配置和登录选项。

4. **完成认证:**
   - 对于OAuth提供商（codex、claude、iflow），系统会自动打开浏览器并等待回调（`sdk/auth/codex.go:82-96`）
   - 对于Gemini，使用Google CLI认证（`sdk/auth/gemini.go:46-50`）
   - 对于Qwen，使用设备流并输入邮箱（`sdk/auth/qwen.go:47-96`）

5. **验证成功:** 登录成功后，认证记录会自动保存到指定目录，文件名格式为 `{provider}-{email}.json`。