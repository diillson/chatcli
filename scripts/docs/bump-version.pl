#!/usr/bin/env perl
# scripts/docs/bump-version.pl — rewrite "current release" version tokens in
# the documentation site (chatcli.ai) when a release is cut.
#
# Usage: OLD_VERSION=1.2.3 NEW_VERSION=1.2.4 perl bump-version.pl file.mdx...
#
# Only tokens that present the CURRENT release are rewritten:
#   - inside fenced code blocks (install commands, sample output)
#   - inside inline code spans (`ghcr.io/diillson/chatcli:1.2.3`)
#   - inside bold spans (**1.2.3**, **Latest version: 1.2.3**)
#   - inside URLs (releases/tag/v1.2.3, download/v1.2.3/...)
# A version in running prose ("As of v1.2.3 the operator…") is history and
# stays as written. The previous global `sed s/OLD/NEW/g` rewrote that
# prose on every release and, because the dots were unescaped, also
# mangled unrelated identifiers such as Bedrock model ids and backup
# file names.
use strict;
use warnings;

my $old = $ENV{OLD_VERSION} // die "OLD_VERSION is required\n";
my $new = $ENV{NEW_VERSION} // die "NEW_VERSION is required\n";
die "OLD_VERSION must look like X.Y.Z\n" unless $old =~ /^\d+\.\d+\.\d+$/;
die "NEW_VERSION must look like X.Y.Z\n" unless $new =~ /^\d+\.\d+\.\d+$/;
exit 0 if $old eq $new;

my $tok = qr/(?<![0-9.])\Q$old\E(?![0-9])/;
my $changed = 0;

for my $path (@ARGV) {
    open my $in, '<:encoding(UTF-8)', $path or die "$path: $!\n";
    my @lines = <$in>;
    close $in;

    my $in_fence = 0;
    my $touched  = 0;
    for my $line (@lines) {
        if ($line =~ /^\s*(```|~~~)/) {
            $in_fence = !$in_fence;
            next;
        }
        my $before = $line;
        if ($in_fence) {
            $line =~ s/$tok/$new/g;
        } else {
            # inline code spans
            1 while $line =~ s/(`[^`\n]*?)$tok(?=[^`\n]*`)/$1$new/;
            # bold spans
            1 while $line =~ s/(\*\*[^*\n]*?)$tok(?=[^*\n]*\*\*)/$1$new/;
            # URLs
            1 while $line =~ s/(https?:\/\/[^\s)<>"']*?)$tok/$1$new/;
        }
        $touched = 1 if $line ne $before;
    }
    next unless $touched;

    open my $out, '>:encoding(UTF-8)', $path or die "$path: $!\n";
    print {$out} @lines;
    close $out;
    $changed++;
    print "updated $path\n";
}
print "bump-version: $changed file(s) updated ($old -> $new)\n";
