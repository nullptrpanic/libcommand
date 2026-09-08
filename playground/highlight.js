const bashKeywords = new Set([
  "!", "[[", "]]", "case", "coproc", "do", "done", "elif", "else", "esac",
  "fi", "for", "function", "if", "in", "select", "then", "time", "until", "while",
]);
const commandStartingKeywords = new Set(["do", "elif", "else", "if", "then", "until", "while"]);
const bashOperators = [";;&", "<<<", "&&", "||", ";;", ";&", "<<", ">>", ">&", "<&", "==", "!=", "=~", "<=", ">=", "((", "))"];

export function tokenizeBash(source) {
  const value = String(source || "");
  const tokens = [];
  let index = 0;
  let commandExpected = true;
  while (index < value.length) {
    const character = value[index];
    if (/\s/.test(character)) {
      const end = consumeWhile(value, index, (current) => /\s/.test(current));
      const whitespace = value.slice(index, end);
      appendToken(tokens, "plain", whitespace);
      if (whitespace.includes("\n")) commandExpected = true;
      index = end;
      continue;
    }
    if (character === "#" && commentStartsAt(value, index)) {
      const newline = value.indexOf("\n", index);
      const end = newline < 0 ? value.length : newline;
      appendToken(tokens, "comment", value.slice(index, end));
      index = end;
      continue;
    }
    if (character === "'") {
      const end = quotedEnd(value, index, "'", false);
      appendToken(tokens, "string", value.slice(index, end));
      index = end;
      continue;
    }
    if (character === '"') {
      index = tokenizeDoubleQuoted(value, index, tokens);
      continue;
    }
    if (character === "`") {
      const end = quotedEnd(value, index, "`", true);
      appendToken(tokens, "variable", value.slice(index, end));
      index = end;
      continue;
    }
    if (character === "$") {
      const end = expansionEnd(value, index);
      appendToken(tokens, "variable", value.slice(index, end));
      index = end;
      continue;
    }
    const operator = operatorAt(value, index);
    if (operator) {
      const kind = bashKeywords.has(operator) ? "keyword" : "operator";
      appendToken(tokens, kind, operator);
      if ([";", ";;", ";&", ";;&", "&&", "||", "|", "&"].includes(operator)) commandExpected = true;
      if (operator === "[[" || operator === "]]" || operator === "((" || operator === "))") commandExpected = false;
      index += operator.length;
      continue;
    }

    const end = consumeWhile(value, index, (current, position) =>
      !/\s/.test(current) && current !== "'" && current !== '"' && current !== "`" &&
      current !== "$" && !operatorAt(value, position));
    const word = value.slice(index, Math.max(index + 1, end));
    let kind = "plain";
    if (bashKeywords.has(word)) {
      kind = "keyword";
      commandExpected = commandStartingKeywords.has(word);
    } else if (/^[A-Za-z_][A-Za-z0-9_]*(?:\[[^\]]+\])?=/.test(word)) {
      kind = "assignment";
    } else if (/^(?:0[xX][0-9A-Fa-f]+|[0-9]+)$/.test(word)) {
      kind = "number";
      commandExpected = false;
    } else if (commandExpected && !word.startsWith("-")) {
      kind = "command";
      commandExpected = false;
    } else {
      commandExpected = false;
    }
    appendToken(tokens, kind, word);
    index += word.length;
  }
  return tokens;
}

function tokenizeDoubleQuoted(source, start, tokens) {
  let index = start;
  let literalStart = start;
  index++;
  while (index < source.length) {
    if (source[index] === "\\") {
      index = Math.min(source.length, index + 2);
      continue;
    }
    if (source[index] === "$" || source[index] === "`") {
      appendToken(tokens, "string", source.slice(literalStart, index));
      const end = source[index] === "$" ? expansionEnd(source, index) : quotedEnd(source, index, "`", true);
      appendToken(tokens, "variable", source.slice(index, end));
      index = end;
      literalStart = index;
      continue;
    }
    if (source[index] === '"') {
      index++;
      appendToken(tokens, "string", source.slice(literalStart, index));
      return index;
    }
    index++;
  }
  appendToken(tokens, "string", source.slice(literalStart));
  return source.length;
}

function expansionEnd(source, start) {
  if (source.startsWith("$(", start)) return balancedEnd(source, start + 1, "(", ")");
  if (source.startsWith("${", start)) return balancedEnd(source, start + 1, "{", "}");
  const match = source.slice(start + 1).match(/^(?:[A-Za-z_][A-Za-z0-9_]*|[0-9]+|[-#?$!@*])/);
  return match ? start + 1 + match[0].length : start + 1;
}

function balancedEnd(source, openingIndex, opening, closing) {
  let depth = 0;
  for (let index = openingIndex; index < source.length; index++) {
    if (source[index] === "\\") {
      index++;
      continue;
    }
    if (source[index] === "'" || source[index] === '"') {
      index = quotedEnd(source, index, source[index], source[index] === '"') - 1;
      continue;
    }
    if (source[index] === opening) depth++;
    if (source[index] === closing && --depth === 0) return index + 1;
  }
  return source.length;
}

function quotedEnd(source, start, quote, escaped) {
  for (let index = start + 1; index < source.length; index++) {
    if (escaped && source[index] === "\\") {
      index++;
      continue;
    }
    if (source[index] === quote) return index + 1;
  }
  return source.length;
}

function operatorAt(source, index) {
  for (const operator of bashOperators) {
    if (source.startsWith(operator, index)) return operator;
  }
  return ";|&()<>".includes(source[index]) ? source[index] : "";
}

function commentStartsAt(source, index) {
  return index === 0 || /[\s;|&()]/.test(source[index - 1]);
}

function consumeWhile(source, start, predicate) {
  let index = start;
  while (index < source.length && predicate(source[index], index)) index++;
  return index;
}

function appendToken(tokens, kind, value) {
  if (!value) return;
  const previous = tokens[tokens.length - 1];
  if (previous?.kind === kind) previous.value += value;
  else tokens.push({ kind, value });
}
