package app.twocan

import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import kotlinx.coroutines.flow.first
import org.junit.Assert.assertEquals
import org.junit.Test

class LogSenderTest {
    // Unconfined: send() runs until the fake upload waits on its gate.
    private val scope = CoroutineScope(Dispatchers.Unconfined)

    @Test
    fun keepsTheCodeAfterSending() = runBlocking {
        val gate = CompletableDeferred<String>()
        var calls = 0
        val sender = LogSender(scope) { note -> calls++; assertEquals("it broke", note); gate.await() }

        sender.send("it broke")
        assertEquals(LogSender.State.Sending, sender.state.value)
        sender.send("again") // ignored while one is on its way
        gate.complete("K7F3-9QX2")

        assertEquals(LogSender.State.Sent("K7F3-9QX2"), withTimeout(1000) { sender.state.first { it is LogSender.State.Sent } })
        assertEquals(1, calls)
    }

    @Test
    fun keepsWhyItFailed() = runBlocking {
        val sender = LogSender(scope) { throw IllegalStateException("the helper isn't accepting logs") }
        sender.send("")
        assertEquals(LogSender.State.Failed("the helper isn't accepting logs"), sender.state.value)
        sender.reset()
        assertEquals(LogSender.State.Idle, sender.state.value)
    }
}
