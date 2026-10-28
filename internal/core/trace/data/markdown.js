(function () {
  "use strict";

  var BT = "`";

  function el(tag, cls) {
    var n = document.createElement(tag);
    if (cls) n.className = cls;
    return n;
  }
  function txt(s) { return document.createTextNode(s); }

  /* Only schemes that cannot execute. Anything else renders as plain text,
   * so a javascript: link in model output is inert rather than clickable. */
  function safeHref(u) {
    var s = String(u || "").trim();
    if (/^(https?:|mailto:|#|\.?\/)/i.test(s)) return s;
    return null;
  }

  /* ------------------------------------------------------------- inline */

  var RE_INLINE = new RegExp(
    BT + "+([\\s\\S]+?)" + BT + "+" +          /* 1 code       */
    "|\\*\\*([\\s\\S]+?)\\*\\*" +           /* 2 strong     */
    "|__([\\s\\S]+?)__" +                     /* 3 strong     */
    "|\\*([^*\\n]+?)\\*" +                    /* 4 em         */
    "|(?:^|\\b)_([^_\\n]+?)_(?:\\b|$)" +       /* 5 em         */
    "|~~([\\s\\S]+?)~~" +                     /* 6 del        */
    "|(!?)\\[([^\\]]*)\\]\\(([^)\\s]*)(?:\\s[^)]*)?\\)" + /* 7 bang 8 text 9 href */
    "|<((?:https?|mailto):[^>\\s]+)>" +          /* 10 autolink  */
    "|(https?://[^\\s<>()\\[\\]\"']+)",         /* 11 bare url  */
    "g");

  /** Inline markup -> a list of nodes.
   *
   * The whole string is scanned FIRST, then the nodes are built: strong/em/link
   * bodies recurse, and RE_INLINE is one shared global regex, so building
   * inside the exec loop would reset its lastIndex mid-scan and the outer loop
   * would match the same span forever. */
  function inline(src) {
    var found = [], m;
    RE_INLINE.lastIndex = 0;
    while ((m = RE_INLINE.exec(src)) !== null) {
      found.push(m);
      if (RE_INLINE.lastIndex === m.index) RE_INLINE.lastIndex++;
    }
    var out = [], last = 0;
    for (var fi = 0; fi < found.length; fi++) {
      m = found[fi];
      if (m.index < last) continue;
      if (m.index > last) out.push(txt(src.slice(last, m.index)));
      last = m.index + m[0].length;
      if (m[1] !== undefined) {
        var c = el("code");
        c.textContent = m[1].replace(/^ | $/g, "");
        out.push(c);
      } else if (m[2] !== undefined || m[3] !== undefined) {
        out.push(wrap("strong", m[2] !== undefined ? m[2] : m[3]));
      } else if (m[4] !== undefined || m[5] !== undefined) {
        out.push(wrap("em", m[4] !== undefined ? m[4] : m[5]));
      } else if (m[6] !== undefined) {
        out.push(wrap("del", m[6]));
      } else if (m[8] !== undefined) {
        var href = safeHref(m[9]);
        var label = m[8] || m[9];
        if (m[7] === "!") label = label ? "image: " + label : "image";
        if (href) {
          var a = el("a", "md-a");
          a.setAttribute("href", href);
          a.setAttribute("rel", "noreferrer noopener");
          a.setAttribute("target", "_blank");
          inline(label).forEach(function (n) { a.appendChild(n); });
          out.push(a);
        } else {
          out.push(txt(label));
        }
      } else if (m[10] !== undefined || m[11] !== undefined) {
        var url = m[10] !== undefined ? m[10] : m[11];
        var la = el("a", "md-a");
        la.setAttribute("href", url);
        la.setAttribute("rel", "noreferrer noopener");
        la.setAttribute("target", "_blank");
        la.textContent = url;
        out.push(la);
      }
    }
    if (last < src.length) out.push(txt(src.slice(last)));
    return out;
  }
  function wrap(tag, src) {
    var n = el(tag);
    inline(src).forEach(function (k) { n.appendChild(k); });
    return n;
  }
  function fill(node, src) {
    inline(src).forEach(function (n) { node.appendChild(n); });
    return node;
  }

  /* -------------------------------------------------------------- blocks */

  var RE_FENCE = new RegExp("^(\\s{0,3})(" + BT + BT + BT + "+|~~~+)\\s*([^\\s" + BT + "]*)");
  var RE_HEAD = /^ {0,3}(#{1,6})\s+(.*?)\s*#*\s*$/;
  var RE_HR = /^ {0,3}([-*_])(?:\s*\1){2,}\s*$/;
  var RE_UL = /^(\s*)([-*+])\s+(.*)$/;
  var RE_OL = /^(\s*)(\d{1,9})[.)]\s+(.*)$/;
  var RE_TASK = /^\[([ xX])\]\s+([\s\S]*)$/;
  var RE_QUOTE = /^ {0,3}>\s?(.*)$/;
  var RE_ROW = /^\s*\|(.+)\|\s*$/;
  var RE_DELIM = /^\s*\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?\s*$/;

  function cells(line) {
    var body = line.replace(/^\s*\|/, "").replace(/\|\s*$/, "");
    var out = [], cur = "", esc = false;
    for (var i = 0; i < body.length; i++) {
      var ch = body.charAt(i);
      if (esc) { cur += ch; esc = false; continue; }
      if (ch === "\\") { esc = true; continue; }
      if (ch === "|") { out.push(cur.trim()); cur = ""; continue; }
      cur += ch;
    }
    out.push(cur.trim());
    return out;
  }

  /** Leading whitespace as a column count, tabs counted as four. */
  function indentOf(s) {
    var n = 0;
    for (var i = 0; i < s.length; i++) {
      var c = s.charAt(i);
      if (c === " ") n++;
      else if (c === "\t") n += 4;
      else break;
    }
    return n;
  }
  function dedent(lines, n) {
    return lines.map(function (l) {
      var k = 0, col = 0;
      while (k < l.length && col < n) {
        var c = l.charAt(k);
        if (c === " ") col++;
        else if (c === "\t") col += 4;
        else break;
        k++;
      }
      return l.slice(k);
    });
  }

  function blocks(lines, into) {
    var i = 0;
    while (i < lines.length) {
      var line = lines[i];
      var enter = i;

      if (!line.trim()) { i++; continue; }

      /* fenced code */
      var f = RE_FENCE.exec(line);
      if (f) {
        var close = f[2].charAt(0), body = [];
        i++;
        while (i < lines.length && !new RegExp("^\\s{0,3}" + (close === "~" ? "~~~" : close + close + close) + "+\\s*$").test(lines[i])) {
          body.push(lines[i]); i++;
        }
        i++;
        var pre = el("pre", "md-pre"), code = el("code");
        if (f[3]) pre.setAttribute("data-lang", f[3]);
        code.textContent = body.join("\n");
        pre.appendChild(code);
        into.appendChild(pre);
        continue;
      }

      /* heading */
      var hd = RE_HEAD.exec(line);
      if (hd) {
        into.appendChild(fill(el("h" + hd[1].length, "md-h"), hd[2]));
        i++;
        continue;
      }

      /* thematic break */
      if (RE_HR.test(line)) { into.appendChild(el("hr", "md-hr")); i++; continue; }

      /* blockquote */
      if (RE_QUOTE.test(line)) {
        var q = [];
        while (i < lines.length && (RE_QUOTE.test(lines[i]) || (q.length && lines[i].trim()))) {
          var qm = RE_QUOTE.exec(lines[i]);
          q.push(qm ? qm[1] : lines[i]);
          i++;
        }
        var bq = el("blockquote", "md-quote");
        blocks(q, bq);
        into.appendChild(bq);
        continue;
      }

      /* table: a pipe row whose next line is the delimiter */
      if (RE_ROW.test(line) && i + 1 < lines.length && RE_DELIM.test(lines[i + 1])) {
        var head = cells(line);
        var align = cells(lines[i + 1]).map(function (c) {
          var l = c.charAt(0) === ":", r = c.charAt(c.length - 1) === ":";
          return l && r ? "center" : r ? "right" : l ? "left" : "";
        });
        i += 2;
        var table = el("table", "md-table"), thead = el("thead"), hr2 = el("tr");
        head.forEach(function (c, k) {
          var th = fill(el("th"), c);
          if (align[k]) th.style.textAlign = align[k];
          hr2.appendChild(th);
        });
        thead.appendChild(hr2); table.appendChild(thead);
        var tbody = el("tbody");
        while (i < lines.length && RE_ROW.test(lines[i])) {
          var tr = el("tr");
          cells(lines[i]).forEach(function (c, k) {
            var td = fill(el("td"), c);
            if (align[k]) td.style.textAlign = align[k];
            tr.appendChild(td);
          });
          tbody.appendChild(tr);
          i++;
        }
        table.appendChild(tbody);
        var scroll = el("div", "md-table-wrap");
        scroll.appendChild(table);
        into.appendChild(scroll);
        continue;
      }

      /* list — items keep their continuation lines, so nesting and
       * paragraphs inside an item come out of the same recursion */
      var lm = RE_UL.exec(line) || RE_OL.exec(line);
      if (lm) {
        var ordered = !RE_UL.exec(line);
        var base = indentOf(line);
        var list = el(ordered ? "ol" : "ul", "md-list");
        if (ordered && Number(lm[2]) !== 1) list.setAttribute("start", lm[2]);
        var loose = false;
        while (i < lines.length) {
          var im = RE_UL.exec(lines[i]) || RE_OL.exec(lines[i]);
          if (!im || indentOf(lines[i]) !== base) break;
          /* a bullet does not continue a numbered list, or the other way round */
          if (!RE_UL.exec(lines[i]) !== ordered) break;
          var itemLines = [im[3]];
          var contIndent = base + 2;
          i++;
          while (i < lines.length) {
            if (!lines[i].trim()) {
              /* a blank line belongs to the item only if indented content follows */
              var j = i + 1;
              while (j < lines.length && !lines[j].trim()) j++;
              if (j < lines.length && indentOf(lines[j]) > base) { itemLines.push(""); loose = true; i = j; continue; }
              break;
            }
            if (indentOf(lines[i]) <= base && (RE_UL.exec(lines[i]) || RE_OL.exec(lines[i]))) break;
            if (indentOf(lines[i]) === 0) break;
            itemLines.push(lines[i]);
            i++;
          }
          var li = el("li", "md-li");
          var body0 = dedent(itemLines, contIndent);
          var task = RE_TASK.exec(body0[0] || "");
          if (task) {
            li.className = "md-li md-task";
            var box = el("input");
            box.setAttribute("type", "checkbox");
            box.setAttribute("disabled", "");
            /* attributes, not properties: the search highlighter restores an
             * element from an innerHTML snapshot and a property would not
             * survive the round trip */
            if (task[1] !== " ") box.setAttribute("checked", "");
            li.appendChild(box);
            body0[0] = task[2];
          }
          var frag = document.createDocumentFragment();
          blocks(body0, frag);
          /* the item's own first paragraph is unwrapped so a tight list —
           * including one whose item carries a nested list — reads as one line
           * rather than a paragraph with a paragraph's margins */
          if (frag.firstChild && frag.firstChild.nodeName === "P") {
            var lead = frag.firstChild;
            while (lead.firstChild) li.appendChild(lead.firstChild);
            frag.removeChild(lead);
          }
          li.appendChild(frag);
          list.appendChild(li);
        }
        if (loose) list.classList.add("md-loose");
        into.appendChild(list);
        continue;
      }

      /* paragraph — runs to the next blank line or block starter */
      var para = [];
      while (i < lines.length && lines[i].trim()) {
        var l2 = lines[i];
        if (para.length && (RE_FENCE.test(l2) || RE_HEAD.test(l2) || RE_HR.test(l2) ||
            RE_QUOTE.test(l2) || RE_UL.test(l2) || RE_OL.test(l2))) break;
        para.push(l2.replace(/\s+$/, ""));
        i++;
      }
      into.appendChild(fill(el("p", "md-p"), para.join("\n")));
      /* belt and braces: no branch above may leave the cursor where it was */
      if (i === enter) i++;
    }
    return into;
  }

  window.__mdRender = function (text) {
    var src = String(text == null ? "" : text).replace(/\r\n?/g, "\n");
    return blocks(src.split("\n"), document.createDocumentFragment());
  };

  /** What the text reads as once the markup punctuation is gone — the string
   * the search box matches and the inline highlighter walks. */
  window.__mdPlain = function (text) {
    var frag = window.__mdRender(text);
    var host = el("div");
    host.appendChild(frag);
    return host.textContent;
  };
})();
