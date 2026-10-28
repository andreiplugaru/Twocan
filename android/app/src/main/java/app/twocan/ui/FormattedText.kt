package app.twocan.ui

import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.LinkAnnotation
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.TextLinkStyles
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextDecoration
import androidx.compose.ui.text.withLink
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.graphics.Color
import app.twocan.Json
import app.twocan.Span
import app.twocan.go.mobile.Mobile

/**
 * Message text with *bold*, _italic_ and tappable links, split up by the
 * Go core (Mobile.format) so it looks as it does on the desktop.
 */
@Composable
fun FormattedText(text: String) {
    val linkColor = MaterialTheme.colorScheme.primary
    val annotated = remember(text, linkColor) {
        val spans = runCatching { Json.spans(Mobile.format(text)) }.getOrElse { listOf(Span(text, false, false, "")) }
        annotate(spans, linkColor)
    }
    Text(annotated)
}

private fun annotate(spans: List<Span>, linkColor: Color): AnnotatedString = buildAnnotatedString {
    val linkStyle = TextLinkStyles(SpanStyle(color = linkColor, textDecoration = TextDecoration.Underline))
    for (s in spans) {
        val style = SpanStyle(
            fontWeight = if (s.bold) FontWeight.Bold else null,
            fontStyle = if (s.italic) FontStyle.Italic else null,
        )
        if (s.url.isNotEmpty()) {
            withLink(LinkAnnotation.Url(s.url, linkStyle)) { append(s.text) }
        } else {
            withStyle(style) { append(s.text) }
        }
    }
}
