// Copyright 2016 The Cockroach Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or
// implied. See the License for the specific language governing
// permissions and limitations under the License.

package cli

import (
	"net/url"
	"strings"
	"testing"

	"github.com/chzyer/readline"
	"github.com/cockroachdb/cockroach/pkg/security"
	"github.com/cockroachdb/cockroach/pkg/testutils/sqlutils"
	"github.com/cockroachdb/cockroach/pkg/util/leaktest"
)

// TestSQLLex tests the usage of the lexer in the sql subcommand.
func TestSQLLex(t *testing.T) {
	defer leaktest.AfterTest(t)()

	c := newCLITest(cliTestParams{t: t})
	defer c.cleanup()

	pgurl, cleanup := sqlutils.PGUrl(t, c.ServingAddr(), t.Name(), url.User(security.RootUser))
	defer cleanup()

	conn := makeSQLConn(pgurl.String())
	defer conn.Close()

	tests := []struct {
		in     string
		expect string
	}{
		{
			in: `
select '
\?
;
';
`,
			expect: `+---------------+
| e'\n\\?\n;\n' |
+---------------+
| ␤             |
| \?␤           |
| ;␤            |
|               |
+---------------+
(1 row)
`,
		},
		{
			in: `
select ''''
;

select '''
;
''';
`,
			expect: `+-------+
| e'\'' |
+-------+
| '     |
+-------+
(1 row)
+--------------+
| e'\'\n;\n\'' |
+--------------+
| '␤           |
| ;␤           |
| '            |
+--------------+
(1 row)
`,
		},
		{
			in: `select 1;
-- just a comment without final semicolon`,
			expect: `+---+
| 1 |
+---+
| 1 |
+---+
(1 row)
`,
		},
	}

	conf := readline.Config{
		DisableAutoSaveHistory: true,
		FuncOnWidthChanged:     func(func()) {},
	}

	// Some other tests (TestDumpRow) mess with this, so make sure it's set.
	cliCtx.tableDisplayFormat = tableDisplayPretty

	for _, test := range tests {
		conf.Stdin = strings.NewReader(test.in)
		out, err := captureOutput(func() {
			err := runInteractive(conn, &conf)
			if err != nil {
				t.Fatal(err)
			}
		})
		if err != nil {
			t.Fatal(err)
		}
		if out != test.expect {
			t.Fatalf("%s:\nexpected: %s\ngot: %s", test.in, test.expect, out)
		}
	}
}

func TestIsEndOfStatement(t *testing.T) {
	defer leaktest.AfterTest(t)()

	tests := []struct {
		in      string
		isEnd   bool
		isEmpty bool
	}{
		{
			in:    ";",
			isEnd: true,
		},
		{
			in:    "; /* comment */",
			isEnd: true,
		},
		{
			in: "; SELECT",
		},
		{
			in: "SELECT",
		},
		{
			in:    "SET; SELECT 1;",
			isEnd: true,
		},
		{
			in:    "SELECT ''''; SET;",
			isEnd: true,
		},
		{
			in:      "  -- hello",
			isEmpty: true,
		},
	}

	for _, test := range tests {
		isEmpty, isEnd := isEndOfStatement(test.in)
		if isEmpty != test.isEmpty {
			t.Errorf("%q: isEmpty expected %v, got %v", test.in, test.isEmpty, isEmpty)
		}
		if isEnd != test.isEnd {
			t.Errorf("%q: isEnd expected %v, got %v", test.in, test.isEnd, isEnd)
		}
	}
}
