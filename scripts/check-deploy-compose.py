#!/usr/bin/env python3
"""只解析编排，不启动容器；使用临时虚构凭据，禁止输出完整配置。需要 Docker Compose v2。"""
import json
import os
from pathlib import Path
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]
DEPLOY = ROOT / "deploy" / "docker"


def render(filename, envfile, valid=True, services=()):
    # 屏蔽调用者真实凭据与部署覆盖，确保结果可重复且不会泄露密钥。
    env = {key: value for key, value in os.environ.items()
           if not key.startswith(("ARKADE_", "COMPOSE_"))}
    command = ["docker", "compose", "--env-file", str(envfile), "-f",
               str(DEPLOY / filename), "config", "--format", "json", *services]
    result = subprocess.run(command, env=env, capture_output=True, text=True)
    if not valid:
        assert result.returncode != 0, f"{filename}: 缺少必填配置却解析成功"
        return None
    # 错误消息也不输出，以免未来 Compose 将变量值写入诊断。
    assert result.returncode == 0, f"{filename}: Compose 解析失败"
    return json.loads(result.stdout)


def check_hub(config):
    hub = config["services"]["hub"]
    env = hub["environment"]
    assert env["ARKADE_DB_PATH"] == "/data/hub.db"
    assert env["ARKADE_WEB_DIST"] == "/app/web/dist"
    assert env["ARKADE_INITIAL_PASS"] == "test-admin-not-a-real-password"
    assert env["ARKADE_AGENT_TOKEN"] == "test-hub-admission-token"
    assert "ARKADE_LOCAL_AGENT_TOKEN" not in env
    assert not any("sock" in volume["target"] or "toml" in volume["target"]
                   for volume in hub["volumes"])
    assert config["volumes"]["hub-data"]["name"] == "arkade-hub-data"


def check_agent(config, token, url):
    agent = config["services"]["agent"]
    env = agent["environment"]
    assert env["ARKADE_AGENT_TOKEN"] == token
    assert env["ARKADE_HUB_URL"] == url
    assert env["ARKADE_DATA_DIR"] == "/opt/arkade/agent"
    assert "ARKADE_INITIAL_PASS" not in env
    assert "ARKADE_LOCAL_AGENT_TOKEN" not in env
    assert any(volume["target"] == "/var/run/docker.sock" for volume in agent["volumes"])
    assert not any("toml" in volume["target"] for volume in agent["volumes"])
    assert config["volumes"]["agent-data"]["name"] == "arkade-agent-data"


def main():
    values = {
        "ARKADE_INITIAL_PASS": "test-admin-not-a-real-password",
        "ARKADE_AGENT_TOKEN": "test-hub-admission-token",
        "ARKADE_LOCAL_AGENT_TOKEN": "test-local-dedicated-token",
        "ARKADE_AGENT_HUB_URL": "wss://hub.example.invalid/agent/connect",
    }
    with tempfile.TemporaryDirectory(prefix="arkade-compose-check-") as directory:
        envfile = Path(directory) / ".env"

        def save(data):
            envfile.write_text("\n".join(f"{k}={v}" for k, v in data.items()) + "\n", encoding="utf-8")

        save(values)
        hub = render("hub-compose.yml", envfile)
        assert set(hub["services"]) == {"hub"}
        check_hub(hub)
        agent = render("agent-compose.yml", envfile)
        assert set(agent["services"]) == {"agent"}
        check_agent(agent, values["ARKADE_AGENT_TOKEN"], values["ARKADE_AGENT_HUB_URL"])
        combined = render("combined-compose.yml", envfile)
        assert set(combined["services"]) == {"hub", "agent"}
        check_hub(combined)
        check_agent(combined, values["ARKADE_LOCAL_AGENT_TOKEN"], "ws://hub:8080/agent/connect")
        assert combined["services"]["agent"]["depends_on"]["hub"]["condition"] == "service_healthy"

        # 首次启动只起 Hub，无需预先取得 Agent 专属密钥。
        save({k: v for k, v in values.items() if k != "ARKADE_LOCAL_AGENT_TOKEN"})
        bootstrap = render("combined-compose.yml", envfile, services=("hub",))
        assert set(bootstrap["services"]) == {"hub"}
        check_hub(bootstrap)
        no_token = render("combined-compose.yml", envfile)
        assert no_token["services"]["agent"]["environment"]["ARKADE_AGENT_TOKEN"] == ""

        for filename, missing in (("hub-compose.yml", "ARKADE_INITIAL_PASS"),
                                  ("hub-compose.yml", "ARKADE_AGENT_TOKEN"),
                                  ("agent-compose.yml", "ARKADE_AGENT_TOKEN"),
                                  ("agent-compose.yml", "ARKADE_AGENT_HUB_URL")):
            save({k: v for k, v in values.items() if k != missing})
            render(filename, envfile, valid=False)

        # 所有面向部署的参数均能从 .env 修改；保留容器内部数据路径和健康检查端口。
        save(dict(values, ARKADE_HUB_PORT="18080", ARKADE_HUB_BIND_IP="127.0.0.1",
                  ARKADE_HUB_VOLUME="test-existing-hub-volume",
                  ARKADE_AGENT_NAME="test-custom-node", ARKADE_AGENT_LOG_LEVEL="debug"))
        custom = render("combined-compose.yml", envfile)
        port = custom["services"]["hub"]["ports"][0]
        assert str(port["published"]) == "18080" and port["target"] == 8080
        assert port["host_ip"] == "127.0.0.1"
        assert custom["volumes"]["hub-data"]["name"] == "test-existing-hub-volume"
        assert custom["services"]["agent"]["environment"]["ARKADE_AGENT_NAME"] == "test-custom-node"
        assert custom["services"]["agent"]["environment"]["ARKADE_LOG_LEVEL"] == "debug"
    print("三套编排解析、密钥隔离、数据卷、健康依赖、首次启动及环境变量覆盖检查通过（未启动容器）。")


if __name__ == "__main__":
    main()
