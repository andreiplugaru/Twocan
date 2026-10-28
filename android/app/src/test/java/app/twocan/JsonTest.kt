package app.twocan

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class JsonTest {
    @Test
    fun contacts() {
        val cs = Json.contacts(
            """[{"id":"12D3KooWA","name":"Ben","state":"pending_incoming","connected":true,"relayed":true,"unread":2}]"""
        )
        assertEquals(listOf(Contact("12D3KooWA", "Ben", "pending_incoming", true, true, 2)), cs)
        assertEquals("wants to add you", cs[0].linkText)
    }

    @Test
    fun linkText() {
        val c = Contact("id", "Ben", "accepted", connected = false, relayed = false, unread = 0)
        assertEquals("offline", c.linkText)
        assertEquals("via relay", c.copy(connected = true, relayed = true).linkText)
        assertEquals("direct", c.copy(connected = true).linkText)
        assertEquals("request sent", c.copy(state = "pending_outgoing").linkText)
    }

    @Test
    fun messages() {
        val ms = Json.messages(
            """[{"id":"0a0b","peer":"12D3KooWA","out":true,"text":"hi","at":1761400000000,"status":"delivered"}]"""
        )
        assertEquals(listOf(Message("0a0b", "12D3KooWA", true, "hi", 1761400000000, "delivered")), ms)
        assertEquals(true, ms[0].delivered)
    }

    @Test
    fun fields() {
        assertEquals(
            listOf(Field("Status", "Offline")),
            Json.fields("""[{"label":"Status","value":"Offline"}]"""),
        )
    }

    @Test
    fun events() {
        assertEquals(
            NodeEvent.MessageArrived(Message("01", "p", false, "yo", 5, "received")),
            Json.event("""{"type":"message","message":{"id":"01","peer":"p","out":false,"text":"yo","at":5,"status":"received"}}"""),
        )
        assertEquals(NodeEvent.Delivered("p", "01"), Json.event("""{"type":"delivered","peer":"p","id":"01"}"""))
        assertEquals(NodeEvent.ContactsChanged, Json.event("""{"type":"contacts"}"""))
        assertEquals(NodeEvent.Reachability("behind NAT"), Json.event("""{"type":"reachability","text":"behind NAT"}"""))
        assertNull(Json.event("""{"type":"something-new"}"""))
        assertEquals(NodeEvent.Updated("p", listOf("01", "02")), Json.event("""{"type":"updated","peer":"p","ids":["01","02"]}"""))
        assertEquals(NodeEvent.Typing("p"), Json.event("""{"type":"typing","peer":"p"}"""))
    }

    @Test
    fun messageWithChatFeatures() {
        val m = Json.message(
            """{"id":"02","peer":"p","out":true,"text":"yes","at":5,"status":"read","replyTo":"01",""" +
                """"editedAt":9,"deleted":false,"myReaction":"","theirReaction":"❤️"}"""
        )
        assertEquals(Message("02", "p", true, "yes", 5, "read", "01", 9, false, "", "❤️"), m)
        assertEquals(true, m.read)
        assertEquals(true, m.edited)
    }

    @Test
    fun spans() {
        assertEquals(
            listOf(Span("a ", false, false, ""), Span("b", true, false, ""), Span("x.y", false, false, "https://x.y")),
            Json.spans("""[{"text":"a ","bold":false,"italic":false,"url":""},{"text":"b","bold":true,"italic":false,"url":""},{"text":"x.y","bold":false,"italic":false,"url":"https://x.y"}]"""),
        )
    }
}
