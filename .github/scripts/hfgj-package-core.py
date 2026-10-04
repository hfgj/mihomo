"""Package a core for Mihomo's existing unpacker and the Verge core updater."""

import argparse
import gzip
from pathlib import Path
import re
import shutil
import zipfile


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=Path)
    parser.add_argument("version")
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]{0,63}", args.version):
        parser.error("invalid version token")
    name = args.binary.name
    if not re.fullmatch(r"mihomo-[a-z0-9-]+(?:\.exe)?", name):
        parser.error("binary must use its unversioned Mihomo asset name")
    windows = name.endswith(".exe")
    base = name[:-4] if windows else name
    args.output.mkdir(parents=True, exist_ok=True)
    package = args.output / f"{base}-{args.version}.{'zip' if windows else 'gz'}"
    if windows:
        info = zipfile.ZipInfo(name)
        info.create_system = 3
        info.external_attr = 0o100755 << 16
        info.compress_type = zipfile.ZIP_DEFLATED
        with zipfile.ZipFile(package, "w", compresslevel=9) as archive:
            with args.binary.open("rb") as source, archive.open(info, "w") as target:
                shutil.copyfileobj(source, target)
    else:
        # The existing updater reads this header to find the unpacked binary.
        # gzip -n would omit it and leave a versioned filename it cannot locate.
        with args.binary.open("rb") as source, package.open("wb") as target:
            with gzip.GzipFile(filename=name, fileobj=target, mode="wb", mtime=0) as archive:
                shutil.copyfileobj(source, archive)
    if package.stat().st_size >= 32 * 1024 * 1024:
        package.unlink()
        parser.error("package exceeds the existing core updater's size limit")
    print(package)


if __name__ == "__main__":
    main()
