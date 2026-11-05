from pathlib import Path

from setuptools import setup

release = Path(__file__).with_name("VERSION").read_text(encoding="utf-8").strip()
base, clawsphere, revision = release.partition("-clawsphere-")
setup(version=f"{base}+clawsphere.{revision}" if clawsphere else release)
