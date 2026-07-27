- 修复内存泄漏问题，该可能导致内存异常占用
- 支持俄语增加翻译范围
- 修复qwen-3.8-max中断问题(mimo也应该属于同一类问题)
- 修复claude模型可能无法识别图片问题 @GGHansome
- 支持自定义Openai端点 @Sxuan-Coder
- WebSearch 接入百度搜索，DuckDuckGo 作为兜底 @杨超
- 修复一些兼容性问题 @kael-odin 
- 修复window上一些表现问题 @philau2512

🔔 如何让AI自动拉模型配置? (以下为提示词，把地址和密钥换为你的)
我的模型配置在 ～/.cursor-local-assistant-v2/config.yaml，我的API地址是：https://xxx 密钥是xxx，帮我拉所有模型配置进去，不要影响已有模型，根据models标准接口拉取。
