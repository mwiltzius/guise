#!/usr/bin/env python3
"""Tiny .docx helper for the end-to-end tests (standard library only).

  docx.py make OUT PARA...        write a document; "|" splits a paragraph
                                  into separate runs, as Word often does
  docx.py text FILE               print the text, one line per paragraph
  docx.py add-part FILE NAME XML  add (or replace) a part in an existing file
"""
import re
import sys
import zipfile
from xml.sax.saxutils import escape

W = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
CONTENT_TYPES = (
    '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
    '<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">'
    '<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>'
    '<Default Extension="xml" ContentType="application/xml"/>'
    '<Override PartName="/word/document.xml" '
    'ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>'
    "</Types>"
)
RELS = (
    '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
    '<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">'
    '<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>'
    "</Relationships>"
)


def make(out, paras):
    body = "".join(
        "<w:p>"
        + "".join(
            f'<w:r><w:rPr><w:b w:val="{i % 2}"/></w:rPr><w:t xml:space="preserve">{escape(run)}</w:t></w:r>'
            for i, run in enumerate(p.split("|"))
        )
        + "</w:p>"
        for p in paras
    )
    doc = (
        '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
        f'<w:document xmlns:w="{W}"><w:body>{body}</w:body></w:document>'
    )
    with zipfile.ZipFile(out, "w", zipfile.ZIP_DEFLATED) as z:
        z.writestr("[Content_Types].xml", CONTENT_TYPES)
        z.writestr("_rels/.rels", RELS)
        z.writestr("word/document.xml", doc)


T = re.compile(r"<w:t(?:\s[^>]*)?>([^<]*)</w:t>")
ENTITIES = {"&amp;": "&", "&lt;": "<", "&gt;": ">", "&quot;": '"', "&apos;": "'"}


def text(path):
    with zipfile.ZipFile(path) as z:
        xml = z.read("word/document.xml").decode()
    for p in re.split(r"</w:p>", xml):
        if "<w:p" not in p:
            continue
        s = "".join(T.findall(p))
        for k, v in ENTITIES.items():
            s = s.replace(k, v)
        print(s)


def add_part(path, name, xml):
    with zipfile.ZipFile(path) as z:
        items = [(i, z.read(i.filename)) for i in z.infolist() if i.filename != name]
    with zipfile.ZipFile(path, "w", zipfile.ZIP_DEFLATED) as z:
        for info, data in items:
            z.writestr(info, data)
        z.writestr(name, xml)


if __name__ == "__main__":
    cmd, args = sys.argv[1], sys.argv[2:]
    {"make": lambda: make(args[0], args[1:]), "text": lambda: text(args[0]),
     "add-part": lambda: add_part(*args)}[cmd]()
