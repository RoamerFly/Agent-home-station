"""Pure-Python raster bridge for ReportLab in the restricted Windows runtime.

The packaged plugin must be able to place a real DOCX image into its PDF even
when Pillow's native ``_imaging`` extension is denied by LPAC.  This module is
deliberately small and conservative: it accepts only bounded baseline JPEGs
and non-interlaced, 8-bit RGB/RGBA PNGs.  Other encodings fail explicitly
instead of being silently dropped or rasterized through an external program.
"""
from __future__ import annotations

from io import BytesIO
import struct
import zlib


class EmbeddedImageError(ValueError):
    pass


MAX_IMAGE_BYTES = 32 * 1024 * 1024
MAX_PIXELS = 20_000_000


def _limited(raw: bytes) -> bytes:
    if not isinstance(raw, bytes) or not raw:
        raise EmbeddedImageError("native_pdf_embedded_image_invalid")
    if len(raw) > MAX_IMAGE_BYTES:
        raise EmbeddedImageError("native_pdf_embedded_image_too_large")
    return raw


def _paeth(left: int, above: int, upper_left: int) -> int:
    value = left + above - upper_left
    left_delta, above_delta, corner_delta = abs(value - left), abs(value - above), abs(value - upper_left)
    return left if left_delta <= above_delta and left_delta <= corner_delta else above if above_delta <= corner_delta else upper_left


def decode_png(raw: bytes) -> dict:
    """Decode a bounded, non-interlaced PNG into ReportLab-safe channels."""
    raw = _limited(raw)
    if not raw.startswith(b"\x89PNG\r\n\x1a\n"):
        raise EmbeddedImageError("native_pdf_embedded_image_unsupported")
    offset, width, height, bit_depth, color_type, interlace = 8, None, None, None, None, None
    payloads, transparent = [], None
    while offset + 12 <= len(raw):
        size = struct.unpack(">I", raw[offset:offset + 4])[0]
        kind = raw[offset + 4:offset + 8]
        start, end = offset + 8, offset + 8 + size
        if end + 4 > len(raw):
            raise EmbeddedImageError("native_pdf_embedded_image_invalid")
        chunk = raw[start:end]
        offset = end + 4
        if kind == b"IHDR":
            if len(chunk) != 13:
                raise EmbeddedImageError("native_pdf_embedded_image_invalid")
            width, height, bit_depth, color_type, compression, filter_method, interlace = struct.unpack(">IIBBBBB", chunk)
            if compression != 0 or filter_method != 0 or bit_depth != 8 or color_type not in (2, 6) or interlace != 0:
                raise EmbeddedImageError("native_pdf_embedded_image_unsupported")
            if not width or not height or width * height > MAX_PIXELS:
                raise EmbeddedImageError("native_pdf_embedded_image_too_large")
        elif kind == b"IDAT":
            payloads.append(chunk)
        elif kind == b"tRNS" and color_type == 2 and len(chunk) == 6:
            transparent = tuple(value >> 8 for value in struct.unpack(">HHH", chunk))
        elif kind == b"IEND":
            break
    if width is None or height is None or not payloads:
        raise EmbeddedImageError("native_pdf_embedded_image_invalid")
    channels = 4 if color_type == 6 else 3
    stride = width * channels
    try:
        decoded = zlib.decompress(b"".join(payloads))
    except zlib.error as exc:
        raise EmbeddedImageError("native_pdf_embedded_image_invalid") from exc
    if len(decoded) != (stride + 1) * height:
        raise EmbeddedImageError("native_pdf_embedded_image_invalid")
    rows, previous, cursor = [], bytearray(stride), 0
    for _ in range(height):
        filter_kind, row = decoded[cursor], bytearray(decoded[cursor + 1:cursor + 1 + stride])
        cursor += stride + 1
        for index in range(stride):
            left = row[index - channels] if index >= channels else 0
            above = previous[index]
            upper_left = previous[index - channels] if index >= channels else 0
            if filter_kind == 1:
                row[index] = (row[index] + left) & 0xFF
            elif filter_kind == 2:
                row[index] = (row[index] + above) & 0xFF
            elif filter_kind == 3:
                row[index] = (row[index] + ((left + above) // 2)) & 0xFF
            elif filter_kind == 4:
                row[index] = (row[index] + _paeth(left, above, upper_left)) & 0xFF
            elif filter_kind != 0:
                raise EmbeddedImageError("native_pdf_embedded_image_unsupported")
        rows.append(bytes(row))
        previous = row
    if color_type == 2:
        return {"width": width, "height": height, "mode": "RGB", "data": b"".join(rows), "alpha": None, "transparent": transparent}
    rgb = bytearray(width * height * 3)
    alpha = bytearray(width * height)
    for source_row, destination_start in zip(rows, range(0, len(rgb), width * 3)):
        alpha_start = destination_start // 3
        for pixel in range(width):
            source = pixel * 4
            destination = destination_start + pixel * 3
            rgb[destination:destination + 3] = source_row[source:source + 3]
            alpha[alpha_start + pixel] = source_row[source + 3]
    return {"width": width, "height": height, "mode": "RGB", "data": bytes(rgb), "alpha": bytes(alpha), "transparent": None}


def _jpeg_dimensions(raw: bytes) -> tuple[int, int, int]:
    raw = _limited(raw)
    if not raw.startswith(b"\xff\xd8"):
        raise EmbeddedImageError("native_pdf_embedded_image_unsupported")
    cursor = 2
    while cursor + 4 <= len(raw):
        if raw[cursor] != 0xFF:
            cursor += 1
            continue
        while cursor < len(raw) and raw[cursor] == 0xFF:
            cursor += 1
        if cursor >= len(raw):
            break
        marker = raw[cursor]
        cursor += 1
        if marker in (0xD8, 0xD9) or 0xD0 <= marker <= 0xD7:
            continue
        if cursor + 2 > len(raw):
            break
        size = struct.unpack(">H", raw[cursor:cursor + 2])[0]
        if size < 2 or cursor + size > len(raw):
            break
        if marker in (0xC0, 0xC1, 0xC2):
            if size < 8:
                break
            precision = raw[cursor + 2]
            height, width = struct.unpack(">HH", raw[cursor + 3:cursor + 7])
            components = raw[cursor + 7]
            if precision != 8 or not width or not height or width * height > MAX_PIXELS or components not in (1, 3):
                raise EmbeddedImageError("native_pdf_embedded_image_unsupported")
            return width, height, components
        cursor += size
    raise EmbeddedImageError("native_pdf_embedded_image_unsupported")


def crop_rgb(image: dict, left: int, top: int, right: int, bottom: int) -> dict:
    """Return a bounded RGB crop for offline OCR; JPEG must be decoded first."""
    if image.get("mode") != "RGB":
        raise EmbeddedImageError("native_pdf_embedded_image_unsupported")
    width, height = image["width"], image["height"]
    left, top, right, bottom = max(0, left), max(0, top), min(width, right), min(height, bottom)
    if right <= left or bottom <= top:
        raise EmbeddedImageError("native_pdf_embedded_image_invalid")
    rows = [image["data"][(row * width + left) * 3:(row * width + right) * 3] for row in range(top, bottom)]
    return {"width": right - left, "height": bottom - top, "mode": "RGB", "data": b"".join(rows), "alpha": None, "transparent": None}


def _reader_class(reader_base):
    class PureImageReader(reader_base):
        def __init__(self, image: dict):
            self.fileName = "document-workshop-image"
            self._image = None
            self._width, self._height = image["width"], image["height"]
            self.mode, self._data = image["mode"], image["data"]
            self._transparent = image.get("transparent")
            alpha = image.get("alpha")
            self._dataA = PureImageReader({"width": self._width, "height": self._height, "mode": "L", "data": alpha, "alpha": None, "transparent": None}) if alpha else None

        def getSize(self):
            return self._width, self._height

        def getRGBData(self):
            return self._data

        def getTransparent(self):
            return self._transparent

        def jpeg_fh(self):
            return None

    return PureImageReader


def reportlab_reader(raw: bytes, filename: str, reader_base):
    """Create an ImageReader-compatible source with no Pillow dependency."""
    if filename.lower().endswith((".jpg", ".jpeg")) or raw.startswith(b"\xff\xd8"):
        width, height, components = _jpeg_dimensions(raw)

        class PureJPEGReader(reader_base):
            def __init__(self):
                self.fileName, self._ident = filename, None
                self._width, self._height, self.mode, self._dataA = width, height, "RGB" if components == 3 else "L", None
                # Canvas uses this for a deterministic XObject name before
                # PDFImageXObject reaches jpeg_fh(); raw JPEG bytes are enough
                # for that identity calculation and do not need decoding.
                self._data = raw

            def getSize(self):
                return self._width, self._height

            def jpeg_fh(self):
                return BytesIO(raw)

            def getRGBData(self):
                return self._data

            def getTransparent(self):
                return None

        return PureJPEGReader()
    return _reader_class(reader_base)(decode_png(raw))


def intrinsic_size(reader) -> tuple[int, int]:
    width, height = reader.getSize()
    if not isinstance(width, int) or not isinstance(height, int) or width <= 0 or height <= 0:
        raise EmbeddedImageError("native_pdf_embedded_image_invalid")
    return width, height
