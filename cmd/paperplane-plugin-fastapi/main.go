// paperplane-plugin-fastapi 是 FastAPI 插件的独立可执行入口。
package main

import (
	sdk "github.com/lemon-paperplane/lemon-paperplane-main/lemon-paperplane-main-plugin-sdk"
	fastapi "github.com/lemon-paperplane/lemon-paperplane-plugin-fastapi/plugin"
)

func main() { sdk.Serve(fastapi.New()) }
