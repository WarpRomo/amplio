/**
 * Copyright 2026 Google LLC
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

// PDF bits shared by the artifact browser (which frames a PDF in the browser's
// built-in viewer) and the markdown rewriter (which links to one).

// isPdfPath reports whether a subpath names a PDF. Extension-only: it decides
// how to RENDER, and the server independently decides the content type it
// serves (by extension, falling back to byte sniffing).
export function isPdfPath(p: string): boolean {
	return p.toLowerCase().endsWith('.pdf');
}

// PDF_MAGIC is the header every PDF starts with. It catches the extensionless
// file that the server sniffs as application/pdf but a filename can't classify.
export const PDF_MAGIC = '%PDF-';

// splitAnchor splits "<subpath>#<anchor>" into its parts. The anchor is a view
// hint for the built-in viewer (#page=7, #zoom=150, #search=foo) rather than
// part of the file's identity, so the viewer keeps it apart from the selection:
// it rides on the frame's URL only.
//
// Only a PDF has anchors the viewer understands, so that's the only place a '#'
// is read as one — elsewhere it's an ordinary character in a filename
// (`notes#1.md`), and splitting there would lose the file.
export function splitAnchor(f: string): { path: string; anchor: string } {
	const i = f.indexOf('#');
	if (i < 0 || !isPdfPath(f.slice(0, i))) return { path: f, anchor: '' };
	return { path: f.slice(0, i), anchor: f.slice(i) };
}
