#!/usr/bin/env python3
"""只解析编排，不启动容器；使用临时虚构凭据，禁止输出完整配置。需要 Docker Compose v2。"""
import json
import os
import re
from pathlib import Path
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]
DEPLOY = ROOT / "deploy" / "docker"


def render(filename, envfile, valid=True, services=()):
    # 屏蔽调用者真实凭据与部署覆盖，确保结果可重复且不会泄露密钥。
    env = {key: value for key, value in os.environ.items()
           if not key.startswith(("ARKADE_", "COMPOSE_"))}
    # 每次只把一份编排与 .env 放到独立临时目录，证明服务器无需仓库或其他文件。
    with tempfile.TemporaryDirectory(prefix="arkade-standalone-") as directory:
        target = Path(directory)
        (target / "compose.yml").write_text((DEPLOY / filename).read_text(encoding="utf-8"), encoding="utf-8")
        (target / ".env").write_text(Path(envfile).read_text(encoding="utf-8"), encoding="utf-8")
        command = ["docker", "compose", "--env-file", ".env", "-f",
                   "compose.yml", "config", "--format", "json", *services]
        result = subprocess.run(command, cwd=target, env=env, capture_output=True, text=True)
    if not valid:
        assert result.returncode != 0, f"{filename}: 缺少必填配置却解析成功"
        return None
    # 错误消息也不输出，以免未来 Compose 将变量值写入诊断。
    assert result.returncode == 0, f"{filename}: Compose 解析失败"
    return json.loads(result.stdout)


def check_hub(config):
    hub = config["services"]["hub"]
    env = hub["environment"]
    assert "ARKADE_DB_PATH" not in env
    assert env["ARKADE_DB_HOST"] == "postgres"
    assert env["ARKADE_DB_PASSWORD"] == "test-pg-not-a-real-password"
    assert env["ARKADE_SETTINGS_KEY_FILE"] == "/data/settings.key"
    assert hub["depends_on"]["postgres"]["condition"] == "service_healthy"
    pg = config["services"]["postgres"]
    assert pg["environment"]["POSTGRES_PASSWORD"] == env["ARKADE_DB_PASSWORD"]
    assert pg["environment"]["POSTGRES_DB"] == env["ARKADE_DB_NAME"]
    assert not pg.get("ports"), "数据库不应暴露宿主端口"
    assert pg["volumes"][0]["target"] == "/var/lib/postgresql/data"
    assert config["volumes"]["postgres-data"]["name"] == "arkade-postgres-data"
    assert env["ARKADE_WEB_DIST"] == "/app/web/dist"
    assert env["ARKADE_INITIAL_PASS"] == "test-admin-not-a-real-password"
    assert env["ARKADE_AGENT_TOKEN"] == ("" if "agent" in config["services"] else "test-hub-admission-token")
    if "agent" in config["services"]:
        assert env["ARKADE_LOCAL_AGENT_TOKEN"] == "test-local-dedicated-token"
        assert env["ARKADE_LOCAL_AGENT_TOKEN"] != env["ARKADE_AGENT_TOKEN"]
    else:
        assert "ARKADE_LOCAL_AGENT_TOKEN" not in env
    assert not any("sock" in volume["target"] or "toml" in volume["target"]
                   for volume in hub["volumes"])
    assert config["volumes"]["hub-data"]["name"] == "arkade-hub-files-pg"


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
        "ARKADE_DB_PASSWORD": "test-pg-not-a-real-password",
        "ARKADE_INITIAL_PASS": "test-admin-not-a-real-password",
        "ARKADE_AGENT_TOKEN": "test-hub-admission-token",
        "ARKADE_LOCAL_AGENT_TOKEN": "test-local-dedicated-token",
    }
    with tempfile.TemporaryDirectory(prefix="arkade-compose-check-") as directory:
        envfile = Path(directory) / ".env"

        def save(data):
            envfile.write_text("\n".join(f"{k}={v}" for k, v in data.items()) + "\n", encoding="utf-8")

        save(values)
        hub = render("hub-compose.yml", envfile)
        assert set(hub["services"]) == {"hub", "postgres"}
        check_hub(hub)
        agent = render("agent-compose.yml", envfile)
        assert set(agent["services"]) == {"agent"}
        check_agent(agent, values["ARKADE_AGENT_TOKEN"], "ws://请填写Hub地址:8080/agent/connect")
        combined = render("combined-compose.yml", envfile)
        assert set(combined["services"]) == {"hub", "agent", "postgres"}
        check_hub(combined)
        check_agent(combined, values["ARKADE_LOCAL_AGENT_TOKEN"], "ws://hub:8080/agent/connect")
        assert combined["services"]["agent"]["depends_on"]["hub"]["condition"] == "service_healthy"

        # 合并部署一步启动：两端使用同一专属密钥，默认名称为本机。
        assert combined["services"]["agent"]["environment"]["ARKADE_AGENT_NAME"] == "本机"
        assert combined["services"]["hub"]["environment"]["ARKADE_LOCAL_AGENT_TOKEN"] == combined["services"]["agent"]["environment"]["ARKADE_AGENT_TOKEN"]
        save({k: v for k, v in values.items() if k != "ARKADE_LOCAL_AGENT_TOKEN"})
        render("combined-compose.yml", envfile, valid=False)

        for filename, missing in (("hub-compose.yml", "ARKADE_DB_PASSWORD"),
                                  ("hub-compose.yml", "ARKADE_INITIAL_PASS"),
                                  ("hub-compose.yml", "ARKADE_AGENT_TOKEN"),
                                  ("agent-compose.yml", "ARKADE_AGENT_TOKEN")):
            save({k: v for k, v in values.items() if k != missing})
            render(filename, envfile, valid=False)

        # 普通配置直接写在编排中，外部旧变量不能意外覆盖。
        save(dict(values, ARKADE_HUB_IMAGE="unexpected:tag", ARKADE_HUB_PORT="18080",
                  ARKADE_HUB_VOLUME="unexpected-volume", ARKADE_AGENT_NAME="unexpected-node"))
        assert render("combined-compose.yml", envfile) == combined
        allowed = {"ARKADE_INITIAL_PASS", "ARKADE_AGENT_TOKEN", "ARKADE_LOCAL_AGENT_TOKEN", "ARKADE_DB_PASSWORD"}
        for filename, config in (("hub-compose.yml", hub), ("agent-compose.yml", agent),
                                 ("combined-compose.yml", combined)):
            source = (DEPLOY / filename).read_text(encoding="utf-8")
            names = set(re.findall(r"\$\{([A-Z_]+)", source))
            assert names <= allowed, f"{filename}: 普通配置不得使用变量插值"
            assert not re.search(r"^\s*(extends|include):", source, re.MULTILINE), "编排必须独立完整"
            assert not any("build" in service for service in config["services"].values())
            for name, service in config["services"].items():
                assert service["image"] == ("postgres:17-alpine" if name == "postgres" else f"ayflying/arkade-{name}:latest")
        examples = {
            "hub.env.example": ("hub-compose.yml", {"ARKADE_INITIAL_PASS", "ARKADE_AGENT_TOKEN", "ARKADE_DB_PASSWORD"}),
            "agent.env.example": ("agent-compose.yml", {"ARKADE_AGENT_TOKEN"}),
            "combined.env.example": ("combined-compose.yml", allowed - {"ARKADE_AGENT_TOKEN"}),
        }
        assert {path.name for path in DEPLOY.glob("*.env.example")} == set(examples)
        assert not (DEPLOY / ".env.example").exists(), "不得保留混用的通用样例"
        for filename, (compose, expected) in examples.items():
            source = (DEPLOY / filename).read_text(encoding="utf-8")
            keys = {line.split("=", 1)[0] for line in source.splitlines()
                    if line.strip() and not line.startswith("#")}
            assert keys == expected, f"{filename}: 凭据必须与对应编排精确匹配"
            # 按每份真实样例的键单独注入虚构值，确保不依赖其他编排的凭据。
            save({key: values[key] for key in keys})
            parsed = render(compose, envfile)
            if "hub" in parsed["services"]:
                check_hub(parsed)
            if "agent" in parsed["services"]:
                combined_mode = compose == "combined-compose.yml"
                check_agent(parsed, values["ARKADE_LOCAL_AGENT_TOKEN" if combined_mode else "ARKADE_AGENT_TOKEN"],
                            "ws://hub:8080/agent/connect" if combined_mode else "ws://请填写Hub地址:8080/agent/connect")
    print("三套编排解析、密钥隔离、数据卷、健康依赖、首次启动及仅凭据变量/无 build 检查通过（未启动容器）。")


if __name__ == "__main__":
    main()
