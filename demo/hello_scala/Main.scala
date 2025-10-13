package hello.scala

import libs.scala.Utils

/** Hello Scala application. */
object Main {
  def main(args: Array[String]): Unit = {
    val message = Utils.formatGreeting("world from Bazel")
    println(message)
    println(s"Version: ${Utils.getVersion}")
  }
}
