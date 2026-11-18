# lemon-paperplane-plugin-fastapi

Lemon Paperplane 的 FastAPI 插件（依赖 python 插件）。

- 识别 `FastAPI(` 应用变量与工厂函数（`--factory`），生成 `python -m uvicorn 模块:变量 --reload --port <port>`。
- 入口脚本自己调用 `uvicorn.run` / `uvicorn.Server`（常在启动前注册路由、建表）时优先直接运行脚本，并从代码、import 的配置文件与 `.env` 推断端口。
- 只是依赖里带了 fastapi（例如桌面程序里一个健康检查端点）时不抢默认运行项。
- 「Uvicorn running on http://0.0.0.0:8000」显示为 http://127.0.0.1:8000；运行工具栏「打开接口文档」（读取 docs_url）。
- 诊断：找不到模块 / 属性、python-multipart、email-validator、缺 uvicorn、端口占用。

构建：`./build.sh`；测试：`go test ./...`。
