package hello.scala

import org.scalatest.funsuite.AnyFunSuite
import libs.scala.Utils

class MainTest extends AnyFunSuite {
  test("formatGreeting should return correct message") {
    val message = Utils.formatGreeting("test")
    assert(message == "Hello, test from Scala!")
  }
  
  test("getVersion should return version string") {
    val version = Utils.getVersion
    assert(version == "1.0.0")
  }
}
