#!/usr/bin/env python3
"""只解析编排，不启动容器；使用临时虚构凭据，禁止输出完整配置。需要 Docker Compose v2。"""
import json
import os
import re
import shutil
from pathlib import Path
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]
DEPLOY = ROOT / "deploy" / "docker"


def render(filename, envfile, valid=True):
    # 屏蔽调用者真实凭据与部署覆盖；每份编排独立于仓库其他文件。
    env = {key: value for key, value in os.environ.items()
           if not key.startswith(("ARKADE_", "COMPOSE_"))}
    with tempfile.TemporaryDirectory(prefix="arkade-standalone-") as directory:
        target = Path(directory)
        (target / "compose.yml").write_text((DEPLOY / filename).read_text(encoding="utf-8"), encoding="utf-8")
        (target / ".env").write_text(Path(envfile).read_text(encoding="utf-8"), encoding="utf-8")
        result = subprocess.run(["docker", "compose", "--env-file", ".env", "-f", "compose.yml", "config", "--format", "json"],
                                cwd=target, env=env, capture_output=True, text=True)
    if not valid:
        assert result.returncode != 0, f"{filename}: 缺少必填配置却解析成功"
        return None
    assert result.returncode == 0, f"{filename}: Compose 解析失败"
    return json.loads(result.stdout)


def check_hub(config, combined=False):
    expected = {"hub", "postgres"} if combined else {"hub"}
    assert set(config["services"]) == expected, "仅合并编排创建 PostgreSQL，不能重复启动独立 Agent"
    hub = config["services"]["hub"]
    env = hub["environment"]
    assert "ARKADE_DB_PATH" not in env
    assert env["ARKADE_DB_HOST"] == ("postgres" if combined else "请填写PostgreSQL地址")
    assert env["ARKADE_DB_PASSWORD"] == "test-pg-not-a-real-password"
    assert env["ARKADE_INITIAL_PASS"] == "test-admin-not-a-real-password"
    assert env["ARKADE_SETTINGS_KEY_FILE"] == "/data/settings.key"
    assert env["ARKADE_WEB_DIST"] == "/app/web/dist"
    assert env["ARKADE_AGENT_TOKEN"] == ""
    assert "ARKADE_LOCAL_AGENT_TOKEN" not in env
    assert env["ARKADE_LOCAL_ENABLED"] == "true"
    assert env["ARKADE_LOCAL_DATA_DIR"] == "/data/local"
    assert env["ARKADE_LOCAL_RUNTIME"] == "auto"
    assert env["ARKADE_LOCAL_ENDPOINT"] == "/var/run/docker.sock"
    assert env["ARKADE_LOCAL_COMPOSE_BIN"] == "docker"
    mounts = {volume["target"]: volume for volume in hub["volumes"]}
    assert set(mounts) == {"/data", "/var/run/docker.sock"}, "Hub 只保留一个数据卷及 Docker socket"
    assert set(config["volumes"]) == ({"hub-data", "postgres-data"} if combined else {"hub-data"})
    assert mounts["/var/run/docker.sock"]["source"] == "/var/run/docker.sock"
    assert mounts["/data"]["source"] == "hub-data"
    assert not any("toml" in target for target in mounts)
    if combined:
        assert hub["depends_on"]["postgres"]["condition"] == "service_healthy"
        pg = config["services"]["postgres"]
        assert pg["environment"]["POSTGRES_PASSWORD"] == env["ARKADE_DB_PASSWORD"]
        assert pg["environment"]["POSTGRES_DB"] == env["ARKADE_DB_NAME"]
        assert not pg.get("ports"), "数据库不应暴露宿主端口"
        assert pg["volumes"][0]["target"] == "/var/lib/postgresql/data"
        assert config["volumes"]["postgres-data"]["name"] == "arkade-postgres-data"
    else:
        assert not hub.get("depends_on"), "Hub 单独编排不能依赖内置数据库"
        assert "postgres-data" not in config.get("volumes", {}), "Hub 单独编排不能声明数据库卷"
    assert config["volumes"]["hub-data"]["name"] == "arkade-hub-data"


def check_agent(config):
    assert set(config["services"]) == {"agent"}
    agent = config["services"]["agent"]
    env = agent["environment"]
    assert env["ARKADE_AGENT_TOKEN"] == "test-remote-dedicated-token"
    assert env["ARKADE_HUB_URL"] == "ws://请填写Hub地址:8080/agent/connect"
    assert env["ARKADE_DATA_DIR"] == "/opt/arkade/agent"
    assert "ARKADE_INITIAL_PASS" not in env
    assert "ARKADE_DB_PASSWORD" not in env
    assert "ARKADE_LOCAL_AGENT_TOKEN" not in env
    assert any(volume["target"] == "/var/run/docker.sock" for volume in agent["volumes"])
    assert not any("toml" in volume["target"] for volume in agent["volumes"])
    assert config["volumes"]["agent-data"]["name"] == "arkade-agent-data"


def main():
    if shutil.which("docker") is None:
        raise SystemExit("需要 Docker Compose v2（未找到 docker）；可在安装 Docker CLI 的 Linux/WSL 中运行本脚本。")
    values = {"ARKADE_DB_PASSWORD": "test-pg-not-a-real-password", "ARKADE_INITIAL_PASS": "test-admin-not-a-real-password",
              "ARKADE_AGENT_TOKEN": "test-remote-dedicated-token"}
    with tempfile.TemporaryDirectory(prefix="arkade-compose-check-") as directory:
        envfile = Path(directory) / ".env"

        def save(data):
            envfile.write_text("\n".join(f"{k}={v}" for k, v in data.items()) + "\n", encoding="utf-8")

        save(values)
        parsed = {name: render(name, envfile) for name in ("hub-compose.yml", "combined-compose.yml", "agent-compose.yml")}
        check_hub(parsed["hub-compose.yml"])
        check_hub(parsed["combined-compose.yml"], combined=True)
        check_agent(parsed["agent-compose.yml"])
        assert parsed["hub-compose.yml"]["name"] == "arkade-hub"
        assert parsed["combined-compose.yml"]["name"] == "arkade"
        for filename, missing in (("hub-compose.yml", "ARKADE_DB_PASSWORD"), ("hub-compose.yml", "ARKADE_INITIAL_PASS"),
                                  ("combined-compose.yml", "ARKADE_DB_PASSWORD"), ("combined-compose.yml", "ARKADE_INITIAL_PASS"),
                                  ("agent-compose.yml", "ARKADE_AGENT_TOKEN")):
            save({key: value for key, value in values.items() if key != missing})
            render(filename, envfile, valid=False)
        # 普通参数直接由 YAML 定义，不允许外部 .env 意外覆盖。
        save(dict(values, ARKADE_LOCAL_ENABLED="false",
                  ARKADE_HUB_IMAGE="unexpected:tag", ARKADE_HUB_PORT="18080", ARKADE_HUB_VOLUME="unexpected-volume"))
        for filename in ("hub-compose.yml", "combined-compose.yml"):
            assert render(filename, envfile) == parsed[filename]
        allowed = {"ARKADE_INITIAL_PASS", "ARKADE_AGENT_TOKEN", "ARKADE_DB_PASSWORD"}
        for filename, config in parsed.items():
            source = (DEPLOY / filename).read_text(encoding="utf-8")
            assert set(re.findall(r"\$\{([A-Z_]+)", source)) <= allowed
            assert not re.search(r"^\s*(extends|include|env_file):", source, re.MULTILINE)
            assert "ARKADE_LOCAL_AGENT_TOKEN" not in source
            for name, service in config["services"].items():
                assert "build" not in service
                assert service["image"] == ("postgres:17-alpine" if name == "postgres" else f"ayflying/arkade-{name}:latest")
        examples = {"hub.env.example": ("hub-compose.yml", {"ARKADE_INITIAL_PASS", "ARKADE_DB_PASSWORD"}),
                    "agent.env.example": ("agent-compose.yml", {"ARKADE_AGENT_TOKEN"}),
                    "combined.env.example": ("combined-compose.yml", {"ARKADE_INITIAL_PASS", "ARKADE_DB_PASSWORD"})}
        assert {path.name for path in DEPLOY.glob("*.env.example")} == set(examples)
        assert not (DEPLOY / ".env.example").exists()
        for filename, (compose, expected) in examples.items():
            source = (DEPLOY / filename).read_text(encoding="utf-8")
            keys = {line.split("=", 1)[0] for line in source.splitlines() if line.strip() and not line.startswith("#")}
            assert keys == expected, f"{filename}: 凭据必须与对应编排精确匹配"
            save({key: values[key] for key in keys})
            config = render(compose, envfile)
            if compose == "agent-compose.yml":
                check_agent(config)
            else:
                check_hub(config, combined=compose == "combined-compose.yml")
    dockerfile = (ROOT / "hub" / "Dockerfile").read_text(encoding="utf-8")
    for fragment in ("COPY agent/go.mod agent/go.sum agent/", "COPY agent ./../agent", "docker-cli docker-cli-compose",
                     "DOCKER_CLI_PLUGIN_EXTRA_DIRS", "/root/.docker/cli-plugins", "docker compose version", "USER root", 'VOLUME ["/data"]'):
        assert fragment in dockerfile, f"Hub 镜像缺少本机执行依赖：{fragment}"
    print("三套编排独立解析、Hub内置本机/无重复Agent、单Hub数据卷、凭据隔离及镜像静态约束通过（未构建镜像或启动容器）。")


if __name__ == "__main__":
    main()
